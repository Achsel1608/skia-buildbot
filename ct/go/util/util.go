// Utility that contains methods for both CT master and worker scripts.
package util

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"go.skia.org/infra/go/exec"
	"go.skia.org/infra/go/util"

	"github.com/skia-dev/glog"
)

const (
	MAX_SYNC_TRIES = 3

	TS_FORMAT = "20060102150405"
)

// GetCTWorkersProd returns an array of all CT workers in the Cluster Telemetry Golo.
func GetCTWorkersProd() []string {
	workers := make([]string, NUM_WORKERS_PROD)
	for i := 0; i < NUM_WORKERS_PROD; i++ {
		workers[i] = fmt.Sprintf(WORKER_NAME_TEMPLATE, i+1)
	}
	return workers
}

// CreateTimestampFile creates a TIMESTAMP file in the specified dir. The dir must
// exist else an error is returned.
func CreateTimestampFile(dir string) error {
	// Create the task file in TaskFileDir.
	timestampFilePath := filepath.Join(dir, TIMESTAMP_FILE_NAME)
	out, err := os.Create(timestampFilePath)
	if err != nil {
		return fmt.Errorf("Could not create %s: %s", timestampFilePath, err)
	}
	defer util.Close(out)
	timestamp := time.Now().UnixNano() / int64(time.Millisecond)
	w := bufio.NewWriter(out)
	if _, err := w.WriteString(strconv.FormatInt(timestamp, 10)); err != nil {
		return fmt.Errorf("Could not write to %s: %s", timestampFilePath, err)
	}
	util.LogErr(w.Flush())
	return nil
}

// CreateTaskFile creates a taskName file in the TaskFileDir dir. It signifies
// that the worker is currently busy doing a particular task.
func CreateTaskFile(taskName string) error {
	// Create TaskFileDir if it does not exist.
	if _, err := os.Stat(TaskFileDir); err != nil {
		if os.IsNotExist(err) {
			// Dir does not exist create it.
			if err := os.MkdirAll(TaskFileDir, 0700); err != nil {
				return fmt.Errorf("Could not create %s: %s", TaskFileDir, err)
			}
		} else {
			// There was some other error.
			return err
		}
	}
	// Create the task file in TaskFileDir.
	taskFilePath := filepath.Join(TaskFileDir, taskName)
	if _, err := os.Create(taskFilePath); err != nil {
		return fmt.Errorf("Could not create %s: %s", taskFilePath, err)
	}
	return nil
}

// DeleteTaskFile deletes a taskName file in the TaskFileDir dir. It should be
// called when the worker is done executing a particular task.
func DeleteTaskFile(taskName string) {
	taskFilePath := filepath.Join(TaskFileDir, taskName)
	if err := os.Remove(taskFilePath); err != nil {
		glog.Errorf("Could not delete %s: %s", taskFilePath, err)
	}
}

func TimeTrack(start time.Time, name string) {
	elapsed := time.Since(start)
	glog.Infof("===== %s took %s =====", name, elapsed)
}

// ExecuteCmd executes the specified binary with the specified args and env. Stdout and Stderr are
// written to stdout and stderr respectively if specified. If not specified then Stdout and Stderr
// will be outputted only to glog.
func ExecuteCmd(binary string, args, env []string, timeout time.Duration, stdout, stderr io.Writer) error {
	return exec.Run(&exec.Command{
		Name:        binary,
		Args:        args,
		Env:         env,
		InheritPath: true,
		Timeout:     timeout,
		LogStdout:   true,
		Stdout:      stdout,
		LogStderr:   true,
		Stderr:      stderr,
	})
}

// SyncDir runs "git pull" and "gclient sync" on the specified directory.
func SyncDir(dir string) error {
	err := os.Chdir(dir)
	if err != nil {
		return fmt.Errorf("Could not chdir to %s: %s", dir, err)
	}

	for i := 0; i < MAX_SYNC_TRIES; i++ {
		if i > 0 {
			glog.Warningf("%d. retry for syncing %s", i, dir)
		}

		err = syncDirStep()
		if err == nil {
			break
		}
		glog.Errorf("Error syncing %s", dir)
	}

	if err != nil {
		glog.Errorf("Failed to sync %s after %d attempts", dir, MAX_SYNC_TRIES)
	}
	return err
}

func syncDirStep() error {
	err := ExecuteCmd(BINARY_GIT, []string{"pull"}, []string{}, GIT_PULL_TIMEOUT, nil, nil)
	if err != nil {
		return fmt.Errorf("Error running git pull: %s", err)
	}
	err = ExecuteCmd(BINARY_GCLIENT, []string{"sync"}, []string{}, GCLIENT_SYNC_TIMEOUT, nil,
		nil)
	if err != nil {
		return fmt.Errorf("Error running gclient sync: %s", err)
	}
	return nil
}

// BuildSkiaTools builds "tools" in the Skia trunk directory.
func BuildSkiaTools() error {
	if err := os.Chdir(SkiaTreeDir); err != nil {
		return fmt.Errorf("Could not chdir to %s: %s", SkiaTreeDir, err)
	}
	// Run "make clean".
	util.LogErr(ExecuteCmd(BINARY_MAKE, []string{"clean"}, []string{}, MAKE_CLEAN_TIMEOUT, nil,
		nil))
	// Build tools.
	return ExecuteCmd(BINARY_MAKE, []string{"tools", "BUILDTYPE=Release"},
		[]string{"GYP_DEFINES=\"skia_warnings_as_errors=0\""}, MAKE_TOOLS_TIMEOUT, nil, nil)
}

// ResetCheckout resets the specified Git checkout.
func ResetCheckout(dir string) error {
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("Could not chdir to %s: %s", dir, err)
	}
	// Run "git reset --hard HEAD"
	resetArgs := []string{"reset", "--hard", "HEAD"}
	util.LogErr(ExecuteCmd(BINARY_GIT, resetArgs, []string{}, GIT_RESET_TIMEOUT, nil, nil))
	// Run "git clean -f -d"
	cleanArgs := []string{"clean", "-f", "-d"}
	util.LogErr(ExecuteCmd(BINARY_GIT, cleanArgs, []string{}, GIT_CLEAN_TIMEOUT, nil, nil))

	return nil
}

// ApplyPatch applies a patch to a Git checkout.
func ApplyPatch(patch, dir string) error {
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("Could not chdir to %s: %s", dir, err)
	}
	// Run "git apply --index -p1 --verbose --ignore-whitespace
	//      --ignore-space-change ${PATCH_FILE}"
	args := []string{"apply", "--index", "-p1", "--verbose", "--ignore-whitespace", "--ignore-space-change", patch}
	return ExecuteCmd(BINARY_GIT, args, []string{}, GIT_APPLY_TIMEOUT, nil, nil)
}

// CleanTmpDir deletes all tmp files from the caller because telemetry tends to
// generate a lot of temporary artifacts there and they take up root disk space.
func CleanTmpDir() {
	files, _ := ioutil.ReadDir(os.TempDir())
	for _, f := range files {
		util.RemoveAll(filepath.Join(os.TempDir(), f.Name()))
	}
}

// Get a direct link to the log of this task on the master's logserver.
func GetMasterLogLink(runID string) string {
	programName := filepath.Base(os.Args[0])
	return fmt.Sprintf("%s/%s.%s.%s.log.INFO.%s", MASTER_LOGSERVER_LINK, programName, MASTER_NAME, CtUser, runID)
}

func GetTimeFromTs(formattedTime string) time.Time {
	t, _ := time.Parse(TS_FORMAT, formattedTime)
	return t
}

func GetCurrentTs() string {
	return time.Now().UTC().Format(TS_FORMAT)
}

// Returns channel that contains all pageset file names without the timestamp
// file and pyc files.
func GetClosedChannelOfPagesets(fileInfos []os.FileInfo) chan string {
	pagesetsChannel := make(chan string, len(fileInfos))
	for _, fileInfo := range fileInfos {
		pagesetName := fileInfo.Name()
		pagesetBaseName := filepath.Base(pagesetName)
		if pagesetBaseName == TIMESTAMP_FILE_NAME || filepath.Ext(pagesetBaseName) == ".pyc" {
			// Ignore timestamp files and .pyc files.
			continue
		}
		pagesetsChannel <- pagesetName
	}
	close(pagesetsChannel)
	return pagesetsChannel
}

// Running benchmarks in parallel leads to multiple chrome instances coming up
// at the same time, when there are crashes chrome processes stick around which
// can severely impact the machine's performance. To stop this from
// happening chrome zombie processes are periodically killed.
func ChromeProcessesCleaner(locker sync.Locker, chromeCleanerTimer time.Duration) {
	for _ = range time.Tick(chromeCleanerTimer) {
		glog.Info("The chromeProcessesCleaner goroutine has started")
		glog.Info("Waiting for all existing tasks to complete before killing zombie chrome processes")
		locker.Lock()
		util.LogErr(ExecuteCmd("pkill", []string{"-9", "chrome"}, []string{}, PKILL_TIMEOUT, nil, nil))
		locker.Unlock()
	}
}

// Contains the data included in CT pagesets.
type PagesetVars struct {
	// A comma separated list of URLs.
	UrlsList string `json:"urls_list"`
	// Will be either "mobile" or "desktop".
	UserAgent string `json:"user_agent"`
	// The location of the web page's WPR data file.
	ArchiveDataFile string `json:"archive_data_file"`
}

func ReadPageset(pagesetPath string) (PagesetVars, error) {
	decodedPageset := PagesetVars{}
	pagesetContent, err := os.Open(pagesetPath)
	if err != nil {
		return decodedPageset, fmt.Errorf("Could not read %s: %s", pagesetPath, err)
	}
	if err := json.NewDecoder(pagesetContent).Decode(&decodedPageset); err != nil {
		return decodedPageset, fmt.Errorf("Could not JSON decode %s: %s", pagesetPath, err)
	}
	return decodedPageset, nil
}

// ValidateSKPs moves all root_dir/dir_name/*.skp into the root_dir and validates them.
// SKPs that fail validation are logged and deleted.
// TODO(rmistry): Parallelize remove_invalid_skps.py
func ValidateSKPs(pathToSkps string) error {
	// List all directories in pathToSkps and copy out the skps.
	skpFileInfos, err := ioutil.ReadDir(pathToSkps)
	if err != nil {
		return fmt.Errorf("Unable to read %s: %s", pathToSkps, err)
	}
	for _, fileInfo := range skpFileInfos {
		if !fileInfo.IsDir() {
			// We are only interested in directories.
			continue
		}
		skpName := fileInfo.Name()
		// Find the largest layer in this directory.
		layerInfos, err := ioutil.ReadDir(filepath.Join(pathToSkps, skpName))
		if err != nil {
			glog.Errorf("Unable to read %s: %s", filepath.Join(pathToSkps, skpName), err)
		}
		if len(layerInfos) > 0 {
			largestLayerInfo := layerInfos[0]
			for _, layerInfo := range layerInfos {
				if layerInfo.Size() > largestLayerInfo.Size() {
					largestLayerInfo = layerInfo
				}
			}
			// Only save SKPs greater than 6000 bytes. Less than that are probably
			// malformed.
			if largestLayerInfo.Size() > 6000 {
				layerPath := filepath.Join(pathToSkps, skpName, largestLayerInfo.Name())
				util.Rename(layerPath, filepath.Join(pathToSkps, skpName+".skp"))
			} else {
				glog.Warningf("Skipping %s because size was less than 6000 bytes", skpName)
			}
		}
		// We extracted what we needed from the directory, now delete it.
		util.RemoveAll(filepath.Join(pathToSkps, skpName))
	}

	glog.Info("Calling remove_invalid_skps.py")
	// Sync Skia tree.
	util.LogErr(SyncDir(SkiaTreeDir))
	// Build tools.
	util.LogErr(BuildSkiaTools())
	// Run remove_invalid_skps.py.
	// Construct path to the python script.
	_, currentFile, _, _ := runtime.Caller(0)
	pathToPyFiles := filepath.Join(
		filepath.Dir((filepath.Dir(filepath.Dir(currentFile)))),
		"py")
	pathToRemoveSKPs := filepath.Join(pathToPyFiles, "remove_invalid_skps.py")
	pathToSKPInfo := filepath.Join(SkiaTreeDir, "out", "Release", "skpinfo")
	args := []string{
		pathToRemoveSKPs,
		"--skp_dir=" + pathToSkps,
		"--path_to_skpinfo=" + pathToSKPInfo,
	}
	util.LogErr(ExecuteCmd("python", args, []string{}, REMOVE_INVALID_SKPS_TIMEOUT, nil, nil))
	return nil
}
