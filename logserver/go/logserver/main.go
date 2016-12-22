// Application that serves up the contents of /tmp/glog via HTTP, giving access
// to logs w/o needing to SSH into the server.
package main

import (
	"bufio"
	"encoding/gob"
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.skia.org/infra/go/common"
	"go.skia.org/infra/go/exec"
	"go.skia.org/infra/go/influxdb"
	"go.skia.org/infra/go/metadata"
	"go.skia.org/infra/go/metrics2"
	"go.skia.org/infra/go/sklog"
	"go.skia.org/infra/go/util"
)

const (
	JS_TEMPLATE = `
		<script type="text/javascript">
			function refreshPage () {
				var bodyElem = document.getElementsByTagName("body")[0];
				var page_y;
				if (bodyElem.scrollTop + window.innerHeight >= bodyElem.scrollHeight) {
					page_y = "end";
				} else {
					page_y = document.getElementsByTagName("body")[0].scrollTop;
				}
				window.location.href = window.location.href.split('?')[0] + '?page_y=' + page_y;
			}

			window.onload = function () {
				setTimeout(refreshPage, %f);
				var req = new XMLHttpRequest();
				req.withCredentials = true
				req.onload = function(){
					%s
					if ( window.location.href.indexOf('page_y') != -1 ) {
						var match = window.location.href.split('?')[1].split("&")[0].split("=");
						var page_y;
						if (match[1] == "end") {
							page_y = document.getElementsByTagName("body")[0].scrollHeight;
						} else {
							page_y = match[1];
						}
						document.getElementsByTagName("body")[0].scrollTop = page_y;
					}
				};
				req.open("get", "%s", true);
				req.send();
			}
		</script>
`
	ADD_RESPONSE_TEXT_PLAIN = `var logNode = document.createTextNode(this.responseText);
					document.getElementById('file_content').appendChild(logNode);`
	ADD_RESPONSE_TEXT_HTML = `document.getElementById('file_content').innerHTML = this.responseText;`
)

var (
	port           = flag.String("port", ":10115", "HTTP service address (e.g., ':10115')")
	dir            = flag.String("dir", "/tmp/glog", "Directory to serve log files from.")
	stateFile      = flag.String("state_file", "/tmp/logserver.state", "File where logserver stores all encountered log files. This ensures that metrics are not duplicated for already processed log files.")
	allowOrigin    = flag.String("allow_origin", "", "Which site this logserver can share data with.")
	reloadDuration = flag.Duration("reload_after", 20*time.Second, "Duration after which the logserver will automatically reload.")

	appLogThreshold = flag.Int64(
		"app_log_threshold", 100*1024*1024,
		"If any app's logs for a log level use up more than app_log_threshold value then the files with the oldest modified time are deleted till size is less than app_log_threshold - app_log_threshold_buffer.")
	appLogThresholdBuffer = flag.Int64(
		"app_log_threshold_buffer", 50*1024*1024,
		"If any app's logs for a log level use up more than app_log_threshold then the files with the oldest modified time are deleted till size is less than app_log_threshold - app_log_threshold_buffer.")
	dirWatchDuration = flag.Duration("dir_watch_duration", 10*time.Second, "How long dir watcher sleeps for before checking the dir.")
	testing          = flag.Bool("testing", false, "Set to true for local testing.")

	enableMetrics  = flag.Bool("enable_metrics", true, "Set to false to disable collecting metrics. Causes the influxdb_* flags to be ignored.")
	influxHost     = flag.String("influxdb_host", influxdb.DEFAULT_HOST, "The InfluxDB hostname.")
	influxUser     = flag.String("influxdb_name", influxdb.DEFAULT_USER, "The InfluxDB username.")
	influxPassword = flag.String("influxdb_password", influxdb.DEFAULT_PASSWORD, "The InfluxDB password.")
	influxDatabase = flag.String("influxdb_database", influxdb.DEFAULT_DATABASE, "The InfluxDB database.")
)

func FileServerWrapperHandler(w http.ResponseWriter, r *http.Request) {
	endpoint := fmt.Sprintf("file_server%s", r.URL.Path)
	// Adjust the path if we are dealing with single or multiple directories.
	for i := 1; i < strings.Count(r.URL.Path, "/"); i++ {
		endpoint = "../" + endpoint
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate, max-age=-1")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	if *allowOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", *allowOrigin)
	}
	// The file_server path could be either plain text or HTML. Regular files are served as
	// plain text, but directories are served as HTML. In the case that the file is plain text,
	// we must escape any HTML special characters.
	addResponseText := ADD_RESPONSE_TEXT_PLAIN
	if strings.HasSuffix(endpoint, "/") {
		// Allow raw HTML for directories.
		addResponseText = ADD_RESPONSE_TEXT_HTML
	}
	fmt.Fprintf(w, fmt.Sprintf(JS_TEMPLATE, (*reloadDuration).Seconds()*1000, addResponseText, endpoint))
	fmt.Fprintf(w, "<pre>")
	fmt.Fprintf(w, "<div id='file_content'></div>")
	fmt.Fprintf(w, "</pre>")
}

// FileServer returns a handler that serves HTTP requests
// with the contents of the file system rooted at root.
//
// To use the operating system's file system implementation,
// use http.Dir:
//
//     http.Handle("/", FileServer(http.Dir("/tmp")))
//
// Differs from net/http FileServer by making directory listings better.
func FileServer(root http.FileSystem) http.Handler {
	return &fileHandler{root}
}

type fileHandler struct {
	root http.FileSystem
}

func (f *fileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upath := r.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
		r.URL.Path = upath
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate, max-age=-1")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	if *allowOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", *allowOrigin)
	}
	serveFile(w, r, f.root, path.Clean(upath))
}

// FileInfoModifiedSlice is for sorting files by their modified time in ascending
// order. Used when cleaning up logs.
type FileInfoModifiedSlice struct {
	fileInfos []os.FileInfo
	// If reverseSort is true then the smaller entry comes after the larger entry.
	reverseSort bool
}

// FileInfoModifiedSlice is for sorting files by their modified time. If a file
// is a symlink then its destination's mod time is used for the sort.
func (p FileInfoModifiedSlice) Len() int { return len(p.fileInfos) }
func (p FileInfoModifiedSlice) Less(i, j int) bool {
	iFileInfo := p.fileInfos[i]
	iModTime := iFileInfo.ModTime()
	if iFileInfo.Mode()&os.ModeSymlink == os.ModeSymlink {
		destFileInfo, err := getSymlinkFileInfo(iFileInfo)
		if err != nil {
			sklog.Errorf("Could not follow %s: %s", iFileInfo.Name(), err)
		} else {
			iModTime = destFileInfo.ModTime()
		}
	}

	jFileInfo := p.fileInfos[j]
	jModTime := jFileInfo.ModTime()
	if jFileInfo.Mode()&os.ModeSymlink == os.ModeSymlink {
		destFileInfo, err := getSymlinkFileInfo(jFileInfo)
		if err != nil {
			sklog.Errorf("Could not follow %s: %s", jFileInfo.Name(), err)
		} else {
			jModTime = destFileInfo.ModTime()
		}
	}

	if p.reverseSort {
		return iModTime.After(jModTime)
	} else {
		return iModTime.Before(jModTime)
	}
}
func (p FileInfoModifiedSlice) Swap(i, j int) {
	p.fileInfos[i], p.fileInfos[j] = p.fileInfos[j], p.fileInfos[i]
}

func getSymlinkFileInfo(fi os.FileInfo) (os.FileInfo, error) {
	dest, err := filepath.EvalSymlinks(filepath.Join(*dir, fi.Name()))
	if err != nil {
		return nil, fmt.Errorf("Broken symlink encountered %s: %s", fi.Name(), err)
	}
	destFileInfo, err := os.Lstat(dest)
	if err != nil {
		return nil, fmt.Errorf("Could not Lstat %s: %s", dest, err)
	}
	return destFileInfo, nil
}

// dirList writes the directory list to the HTTP response.
//
// glog convention is that log files are created in the following format:
// "ingest.skia-testing-b.perf.log.ERROR.20141015-133007.3273"
// where the first word is the name of the app.
// glog also creates symlinks that look like "ingest.ERROR". These
// symlinks point to the latest log type.
// This method displays sorted symlinks first and then displays sorted sections for
// all apps. Files and directories not in the glog format are bucketed into an
// "unknown" app.
func dirList(w http.ResponseWriter, f http.File) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<pre>\n")
	// Datastructures to populate and output.
	topLevelSymlinks := make([]os.FileInfo, 0)
	topLevelSymlinkMaxFileName := 0
	appToLogs := make(map[string][]os.FileInfo)
	appToMaxFileName := make(map[string]int)
	for {
		fileInfos, err := f.Readdir(10000)
		if err != nil || len(fileInfos) == 0 {
			break
		}
		// Prepopulate the datastructures.
		for _, fileInfo := range fileInfos {
			name := fileInfo.Name()
			nameTokens := strings.Split(name, ".")
			if len(nameTokens) == 2 {
				topLevelSymlinks = append(topLevelSymlinks, fileInfo)
				if len(fileInfo.Name()) > topLevelSymlinkMaxFileName {
					topLevelSymlinkMaxFileName = len(fileInfo.Name())
				}
			} else if len(nameTokens) > 1 {
				appToLogs[nameTokens[0]] = append(appToLogs[nameTokens[0]], fileInfo)
				if len(fileInfo.Name()) > appToMaxFileName[nameTokens[0]] {
					appToMaxFileName[nameTokens[0]] = len(fileInfo.Name())
				}
			} else {
				// File all directories or files created by something other than
				// glog under "unknown" app.
				appToLogs["unknown"] = append(appToLogs["unknown"], fileInfo)
			}
		}
	}

	// First output the top level symlinks.
	sort.Sort(FileInfoModifiedSlice{fileInfos: topLevelSymlinks, reverseSort: true})
	for _, fileInfo := range topLevelSymlinks {
		writeFileInfo(w, fileInfo, topLevelSymlinkMaxFileName)
	}
	// Second output app links to their anchors.
	var keys []string
	for k := range appToLogs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) != 0 {
		fmt.Fprint(w, "\nJump to sections:\n")
	}
	for _, app := range keys {
		fmt.Fprintf(w, "<a href=\"#%s\">%s</a>\n", app, template.HTMLEscapeString(app))
	}
	fmt.Fprint(w, "\n")
	// Then output the logs of all the different apps.
	for _, app := range keys {
		appFileInfos := appToLogs[app]
		sort.Sort(FileInfoModifiedSlice{fileInfos: appFileInfos, reverseSort: true})
		fmt.Fprintf(w, "\n===== <a name=\"%s\">%s</a> =====\n\n", app, template.HTMLEscapeString(app))
		for _, fileInfo := range appFileInfos {
			writeFileInfo(w, fileInfo, appToMaxFileName[app])
		}
	}
	fmt.Fprintf(w, "</pre>\n")
}

func writeFileInfo(w http.ResponseWriter, fileInfo os.FileInfo, maxFileName int) {
	name := fileInfo.Name()
	if fileInfo.IsDir() {
		name += "/"
	}

	url := url.URL{Path: name}
	fileLinks := ""
	if !fileInfo.IsDir() {
		fileSize := util.GetFormattedByteSize(float64(fileInfo.Size()))
		buffer := strings.Repeat(" ", maxFileName-len(fileInfo.Name()))
		staticViewLink := fmt.Sprintf("<a href='file_server/%s'>static</a>", url.String())
		downloadLink := fmt.Sprintf("<a href='file_server/%s' download='file_server/%s'>download</a>", url.String(), template.HTMLEscapeString(name))
		fileLinks = fmt.Sprintf("%s(%s  %s  %s)", buffer, fileSize, staticViewLink, downloadLink)
	}
	modTime := fileInfo.ModTime()
	// Use the destination file's mode time if it is a symlink.
	if fileInfo.Mode()&os.ModeSymlink == os.ModeSymlink {
		destFileInfo, err := getSymlinkFileInfo(fileInfo)
		if err != nil {
			sklog.Errorf("Could not follow %s: %s", name, err)
		} else {
			modTime = destFileInfo.ModTime()
		}
	}
	fmt.Fprintf(w, "%s <a href=\"%s\">%s</a>  %s\n", modTime.Round(time.Second), url.String(), template.HTMLEscapeString(name), fileLinks)
}

func serveFile(w http.ResponseWriter, r *http.Request, fs http.FileSystem, name string) {
	f, err := fs.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer util.Close(f)

	d, err1 := f.Stat()
	if err1 != nil {
		http.NotFound(w, r)
		return
	}

	url := r.URL.Path
	if d.IsDir() {
		if url[len(url)-1] != '/' {
			w.Header().Set("Location", path.Base(url)+"/")
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
		sklog.Infof("Dir List: %s", name)
		dirList(w, f)
		return
	}

	http.ServeContent(w, r, d.Name(), d.ModTime(), f)
}

// getAppAndLogLevel returns the app name and the log level of the specified
// glog file by parsing it.
// It expects a structure that looks like this:
// "ingest.skia-testing-b.perf.log.ERROR.20141015-133007.3273"
func getAppAndLogLevel(fileInfo os.FileInfo) (string, string) {
	name := fileInfo.Name()
	nameTokens := strings.Split(name, ".")
	if len(nameTokens) > 5 {
		return nameTokens[0], nameTokens[4]
	}
	// Ignore symlinks and other logs not created by sklog.
	return "", ""
}

type fileState struct {
	LineCount int64
	Size      int64
}

type logserverState struct {
	FilesToState       map[string]fileState
	AppLogLevelToSpace map[string]int64
	AppLogLevelToCount map[string]int64
	LastCompletedRun   time.Time
}

func getPreviousState() (map[string]fileState, map[string]int64, map[string]int64, time.Time, error) {
	if _, err := os.Stat(*stateFile); os.IsNotExist(err) {
		// State file does not exist, return empty values.
		return map[string]fileState{}, map[string]int64{}, map[string]int64{}, time.Time{}, nil
	}
	f, err := os.Open(*stateFile)
	if err != nil {
		sklog.Errorf("Failed to open old state file %s for reading: %s", *stateFile, err)
		// Delete it and return empty values.
		if err := os.Remove(*stateFile); err != nil {
			return nil, nil, nil, time.Time{}, fmt.Errorf("Could not delete old state file %s: %s", *stateFile, err)
		}
		sklog.Errorf("Deleted old state file %s", *stateFile)
		return map[string]fileState{}, map[string]int64{}, map[string]int64{}, time.Time{}, nil
	}
	defer util.Close(f)
	state := &logserverState{}
	dec := gob.NewDecoder(f)
	if err := dec.Decode(state); err != nil {
		sklog.Errorf("Failed to decode old state file %s: %s", *stateFile, err)
		// Delete it and return empty values.
		if err := os.Remove(*stateFile); err != nil {
			return nil, nil, nil, time.Time{}, fmt.Errorf("Could not delete old state file %s: %s", *stateFile, err)
		}
		sklog.Errorf("Deleted old state file %s", *stateFile)
		return map[string]fileState{}, map[string]int64{}, map[string]int64{}, time.Time{}, nil
	}
	return state.FilesToState, state.AppLogLevelToSpace, state.AppLogLevelToCount, state.LastCompletedRun, nil
}

func writeCurrentState(filestoState map[string]fileState, appLogLevelToSpace, appLogLevelToCount map[string]int64, lastCompletedRun time.Time) error {
	f, err := os.Create(*stateFile)
	if err != nil {
		return fmt.Errorf("Unable to create state file %s: %s", *stateFile, err)
	}
	defer util.Close(f)
	state := &logserverState{
		FilesToState:       filestoState,
		AppLogLevelToSpace: appLogLevelToSpace,
		AppLogLevelToCount: appLogLevelToCount,
		LastCompletedRun:   lastCompletedRun,
	}
	enc := gob.NewEncoder(f)
	if err := enc.Encode(state); err != nil {
		return fmt.Errorf("Failed to encode state: %s", err)
	}
	return nil
}

func getLineCount(path string) int64 {
	file, _ := os.Open(path)
	fileScanner := bufio.NewScanner(file)
	var lineCount int64
	for fileScanner.Scan() {
		lineCount++
	}
	return lineCount
}

// dirWatcher watches for changes in the specified dir. The frequency of polling
// is determined by the duration parameter. dirWatcher ensures:
// * Each app's logs do not exceed the log limit threshold. If they do then the
//   oldest files are deleted.
// * New encountered logs are reported to InfluxDB.
func dirWatcher(duration time.Duration, dir string) {
	filesToState, appLogLevelToSpace, appLogLevelToCount, lastCompletedRun, err := getPreviousState()
	if err != nil {
		sklog.Fatalf("Could get access previous state: %s", err)
	}
	appLogLevelToMetric := make(map[string]metrics2.Int64Metric)
	updatedFiles := false
	markFn := func(path string, fileInfo os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fileInfo.IsDir() || fileInfo.Mode()&os.ModeSymlink != 0 {
			// We are only interested in watching non-symlink log files in the
			// top-level dir.
			return nil
		}

		if _, exists := filesToState[path]; !exists || fileInfo.ModTime().After(lastCompletedRun) {
			sklog.Infof("Processing %s", path)
			app, logLevel := getAppAndLogLevel(fileInfo)
			if app != "" && logLevel != "" {
				appLogLevel := fmt.Sprintf("%s.%s", app, logLevel)
				if _, ok := appLogLevelToMetric[appLogLevel]; !ok {
					// First time encountered this app and log level combination.
					// Create a counter metric.
					appLogLevelToMetric[appLogLevel] = metrics2.GetInt64Metric("logs", map[string]string{"level": logLevel, "name": app})
				}

				// Calculate how many new lines and new disk space usage there is.
				totalLines := getLineCount(path)
				totalSize := fileInfo.Size()
				newLines := totalLines
				newSpace := totalSize
				if exists {
					fileState := filesToState[path]
					newLines = totalLines - fileState.LineCount
					newSpace = totalSize - fileState.Size
				}

				sklog.Infof("Processed %d new lines", newLines)
				sklog.Infof("Processed %d new bytes", newSpace)

				// Update the logs count metric.
				appLogLevelToCount[appLogLevel] += newLines
				appLogLevelToMetric[appLogLevel].Update(appLogLevelToCount[appLogLevel])

				// Add the file size to the current space count for this app and
				// log level combination.
				appLogLevelToSpace[appLogLevel] += newSpace

				updatedFiles = true
			}
			filesToState[path] = fileState{LineCount: getLineCount(path), Size: fileInfo.Size()}
		}
		return nil
	}

	for _ = range time.Tick(duration) {
		if err := filepath.Walk(dir, markFn); err != nil {
			sklog.Fatal(err)
		}
		deletedFiles := cleanupAppLogs(dir, appLogLevelToSpace, filesToState)
		if updatedFiles || deletedFiles {
			if err := writeCurrentState(filesToState, appLogLevelToSpace, appLogLevelToCount, time.Now()); err != nil {
				sklog.Fatalf("Could not write state: %s", err)
			}
			sklog.Info(getPrettyMap(appLogLevelToCount, "AppLogLevels to their line counts"))
			sklog.Info(getPrettyMap(appLogLevelToSpace, "AppLogLevels to their disk space"))
		}
		updatedFiles = false
		lastCompletedRun = time.Now()
	}
}

func getPrettyMap(m map[string]int64, name string) string {
	log := name + ": {"
	for k := range m {
		log += fmt.Sprintf("%s: %d, ", k, m[k])
	}
	log = strings.TrimRight(log, ", ")
	log += "}"
	return log
}

func cleanupAppLogs(dir string, appLogLevelToSpace map[string]int64, filesToState map[string]fileState) bool {
	deletedFiles := false
	for appLogLevel := range appLogLevelToSpace {
		if appLogLevelToSpace[appLogLevel] > *appLogThreshold {
			sklog.Infof("App %s is above the threshold. Usage: %d. Threshold: %d", appLogLevel, appLogLevelToSpace[appLogLevel], *appLogThreshold)
			tokens := strings.Split(appLogLevel, ".")
			app := tokens[0]
			logLevel := tokens[1]
			logGlob := filepath.Join(dir, app+".*"+logLevel+".*")
			matches, err := filepath.Glob(logGlob)
			if err != nil {
				sklog.Fatalf("Could not glob for %s: %s", logGlob, err)
			}
			fileInfos := make([]os.FileInfo, len(matches))
			for i, match := range matches {
				fileInfo, err := os.Stat(match)
				if err != nil {
					sklog.Fatalf("Could not stat %s: %s", match, err)
				}
				fileInfos[i] = fileInfo
			}

			// Sort by Modified time and keep deleting till we are at
			// (threshold - buffer) space left.
			sort.Sort(FileInfoModifiedSlice{fileInfos: fileInfos, reverseSort: false})
			index := 0
			for appLogLevelToSpace[appLogLevel] > *appLogThreshold-*appLogThresholdBuffer {
				if index+1 == len(fileInfos) {
					sklog.Warningf("App %s is above the threshold and has only one file remaining: %s. Not deleting it.", appLogLevel, fileInfos[index].Name())
					break
				}
				fileName := fileInfos[index].Name()
				appLogLevelToSpace[appLogLevel] -= fileInfos[index].Size()
				if err = os.Remove(filepath.Join(dir, fileName)); err != nil {
					sklog.Fatalf("Could not delete %s: %s", fileName, err)
				}
				// Remove the entry from the filesToState map.
				delete(filesToState, filepath.Join(dir, fileName))
				deletedFiles = true
				sklog.Infof("Deleted %s", fileName)
				index++
			}
			// Just incase we delete a massive log file.
			if appLogLevelToSpace[appLogLevel] < 0 {
				appLogLevelToSpace[appLogLevel] = 0
			}
		}
	}
	return deletedFiles
}

// GoogleLoggingAuthInit downloads the JWT service account stored in GCE project
// level metadata and stores it where the Google Logging fluentd client expects to
// find it. This function may fail if the directory for the credentials hasn't
// been created, which is fine since not all machines are setup to use Google
// Logging yet, which is why we only log warnings and don't return an error.
//
// See https://cloud.google.com/logging/docs/agent/authorization.
func GoogleLoggingAuthInit() {
	jwt, err := metadata.ProjectGet(metadata.JWT_SERVICE_ACCOUNT)
	if err != nil {
		sklog.Warningf("Failed to download the jwt from metadata: %s", err)
		return
	}
	f, err := os.OpenFile("/etc/google/auth/application_default_credentials.json", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		sklog.Warningf("Failed to open Logging credentials file for writing: %s", err)
		return
	}
	defer util.Close(f)
	_, err = f.WriteString(jwt)
	if err != nil {
		sklog.Warningf("Failed to write Logging credentials: %s", err)
		return
	}
	restartCmd := &exec.Command{
		Name:        "sudo",
		Args:        []string{"/etc/init.d/google-fluentd restart"},
		InheritPath: true,
		LogStderr:   true,
		LogStdout:   true,
	}
	if err = exec.Run(restartCmd); err != nil {
		sklog.Warningf("Failed to restart fluentd: %s", err)
		return
	}
}

func main() {
	defer common.LogPanic()
	common.Init()
	go GoogleLoggingAuthInit()
	if *enableMetrics {
		common.StartMetrics2("logserver", influxHost, influxUser, influxPassword, influxDatabase, *testing)
	}

	if err := os.MkdirAll(*dir, 0777); err != nil {
		sklog.Fatalf("Failed to create dir for log files: %s", err)
	}

	go dirWatcher(*dirWatchDuration, *dir)

	http.Handle("/file_server/", http.StripPrefix("/file_server/", FileServer(http.Dir(*dir))))
	http.HandleFunc("/", FileServerWrapperHandler)
	sklog.Fatal(http.ListenAndServe(*port, nil))
}
