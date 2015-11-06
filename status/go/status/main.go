/*
	Provides roll-up statuses for Skia build/test/perf.
*/

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode"
)

import (
	"github.com/gorilla/mux"
	"github.com/skia-dev/glog"
)

import (
	"go.skia.org/infra/go/autoroll"
	"go.skia.org/infra/go/buildbot"
	"go.skia.org/infra/go/common"
	"go.skia.org/infra/go/gitinfo"
	"go.skia.org/infra/go/influxdb"
	"go.skia.org/infra/go/login"
	"go.skia.org/infra/go/metadata"
	"go.skia.org/infra/go/rietveld"
	"go.skia.org/infra/go/skiaversion"
	"go.skia.org/infra/go/timer"
	"go.skia.org/infra/go/util"
	"go.skia.org/infra/status/go/commit_cache"
	"go.skia.org/infra/status/go/device_cfg"
)

const (
	DEFAULT_COMMITS_TO_LOAD = 50
	SKIA_REPO               = "skia"
	INFRA_REPO              = "infra"
	GOLD_STATUS_QUERY_TMPL  = "select value from \"status.untriaged.by_corpus.%s.value\" where app='skiacorrectness' and host='skia-gold-prod' order by time desc limit 1"
)

var (
	commitCaches         map[string]*commit_cache.CommitCache = nil
	buildbotDashTemplate *template.Template                   = nil
	commitsTemplate      *template.Template                   = nil
	hostsTemplate        *template.Template                   = nil
	infraTemplate        *template.Template                   = nil
	dbClient             *influxdb.Client                     = nil
	goldGMStatus         *util.PollingStatus                  = nil
	goldSKPStatus        *util.PollingStatus                  = nil
	goldImageStatus      *util.PollingStatus                  = nil
	perfStatus           *util.PollingStatus                  = nil
	rollStatus           *util.PollingStatus                  = nil
	slaveHosts           *util.PollingStatus                  = nil
	androidDevices       *util.PollingStatus                  = nil
	sshDevices           *util.PollingStatus                  = nil
)

// flags
var (
	graphiteServer = flag.String("graphite_server", "localhost:2003", "Where is Graphite metrics ingestion server running.")
	host           = flag.String("host", "localhost", "HTTP service host")
	port           = flag.String("port", ":8002", "HTTP service port (e.g., ':8002')")
	useMetadata    = flag.Bool("use_metadata", true, "Load sensitive values from metadata not from flags.")
	testing        = flag.Bool("testing", false, "Set to true for locally testing rules. No email will be sent.")
	workdir        = flag.String("workdir", ".", "Directory to use for scratch work.")
	resourcesDir   = flag.String("resources_dir", "", "The directory to find templates, JS, and CSS files. If blank the current directory will be used.")
)

// StringIsInteresting returns true iff the string contains non-whitespace characters.
func StringIsInteresting(s string) bool {
	for _, c := range s {
		if !unicode.IsSpace(c) {
			return true
		}
	}
	return false
}

func reloadTemplates() {
	// Change the current working directory to two directories up from this source file so that we
	// can read templates and serve static (res/) files.

	if *resourcesDir == "" {
		_, filename, _, _ := runtime.Caller(0)
		*resourcesDir = filepath.Join(filepath.Dir(filename), "../..")
	}
	commitsTemplate = template.Must(template.ParseFiles(
		filepath.Join(*resourcesDir, "templates/commits.html"),
		filepath.Join(*resourcesDir, "templates/header.html"),
	))
	hostsTemplate = template.Must(template.ParseFiles(
		filepath.Join(*resourcesDir, "templates/hosts.html"),
		filepath.Join(*resourcesDir, "templates/header.html"),
	))
	infraTemplate = template.Must(template.ParseFiles(
		filepath.Join(*resourcesDir, "templates/infra.html"),
		filepath.Join(*resourcesDir, "templates/header.html"),
	))
	buildbotDashTemplate = template.Must(template.ParseFiles(
		filepath.Join(*resourcesDir, "templates/buildbot_dash.html"),
		filepath.Join(*resourcesDir, "templates/header.html"),
	))
}

func Init() {
	reloadTemplates()
}

func userHasEditRights(r *http.Request) bool {
	return strings.HasSuffix(login.LoggedInAs(r), "@google.com")
}

func getIntParam(name string, r *http.Request) (*int, error) {
	raw, ok := r.URL.Query()[name]
	if !ok {
		return nil, nil
	}
	v64, err := strconv.ParseInt(raw[0], 10, 32)
	if err != nil {
		return nil, fmt.Errorf("Invalid value for parameter %q: %s -- %v", name, raw, err)
	}
	v32 := int(v64)
	return &v32, nil
}

func getCommitCache(w http.ResponseWriter, r *http.Request) (*commit_cache.CommitCache, error) {
	repo, _ := mux.Vars(r)["repo"]
	cache, ok := commitCaches[repo]
	if !ok {
		e := fmt.Sprintf("Unknown repo: %s", repo)
		err := fmt.Errorf(e)
		util.ReportError(w, r, err, e)
		return nil, err
	}
	return cache, nil
}

func commitsJsonHandler(w http.ResponseWriter, r *http.Request) {
	defer timer.New("commitsJsonHandler").Stop()
	w.Header().Set("Content-Type", "application/json")
	cache, err := getCommitCache(w, r)
	if err != nil {
		return
	}
	commitsToLoad := DEFAULT_COMMITS_TO_LOAD
	n, err := getIntParam("n", r)
	if err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Invalid parameter: %v", err))
		return
	}
	if n != nil {
		commitsToLoad = *n
	}
	if err := cache.LastNAsJson(w, commitsToLoad); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to load commits from cache: %v", err))
		return
	}
}

func addBuildCommentHandler(w http.ResponseWriter, r *http.Request) {
	defer timer.New("addBuildCommentHandler").Stop()
	if !userHasEditRights(r) {
		util.ReportError(w, r, fmt.Errorf("User does not have edit rights."), "User does not have edit rights.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	cache, err := getCommitCache(w, r)
	if err != nil {
		return
	}
	buildId, err := strconv.ParseInt(mux.Vars(r)["buildId"], 10, 32)
	if err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Invalid build id: %v", err))
		return
	}
	comment := struct {
		Comment string `json:"comment"`
	}{}
	if err := json.NewDecoder(r.Body).Decode(&comment); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to add comment: %v", err))
		return
	}
	defer util.Close(r.Body)
	c := buildbot.BuildComment{
		BuildId:   int(buildId),
		User:      login.LoggedInAs(r),
		Timestamp: float64(time.Now().UTC().Unix()),
		Message:   comment.Comment,
	}
	if err := cache.AddBuildComment(int(buildId), &c); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to add comment: %v", err))
		return
	}
}

func deleteBuildCommentHandler(w http.ResponseWriter, r *http.Request) {
	defer timer.New("deleteBuildCommentHandler").Stop()
	if !userHasEditRights(r) {
		util.ReportError(w, r, fmt.Errorf("User does not have edit rights."), "User does not have edit rights.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	cache, err := getCommitCache(w, r)
	if err != nil {
		return
	}
	buildId, err := strconv.ParseInt(mux.Vars(r)["buildId"], 10, 32)
	if err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Invalid build id: %v", err))
		return
	}
	commentId, err := strconv.ParseInt(mux.Vars(r)["commentId"], 10, 32)
	if err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Invalid comment id: %v", err))
		return
	}
	if err := cache.DeleteBuildComment(int(buildId), int(commentId)); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to delete comment: %v", err))
		return
	}
}

func addBuilderCommentHandler(w http.ResponseWriter, r *http.Request) {
	defer timer.New("addBuilderCommentHandler").Stop()
	if !userHasEditRights(r) {
		util.ReportError(w, r, fmt.Errorf("User does not have edit rights."), "User does not have edit rights.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	cache, err := getCommitCache(w, r)
	if err != nil {
		return
	}
	builder := mux.Vars(r)["builder"]

	comment := struct {
		Comment       string `json:"comment"`
		Flaky         bool   `json:"flaky"`
		IgnoreFailure bool   `json:"ignoreFailure"`
	}{}
	if err := json.NewDecoder(r.Body).Decode(&comment); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to add comment: %v", err))
		return
	}
	defer util.Close(r.Body)

	c := buildbot.BuilderComment{
		Builder:       builder,
		User:          login.LoggedInAs(r),
		Timestamp:     float64(time.Now().UTC().Unix()),
		Flaky:         comment.Flaky,
		IgnoreFailure: comment.IgnoreFailure,
		Message:       comment.Comment,
	}
	if err := cache.AddBuilderComment(builder, &c); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to add builder comment: %v", err))
		return
	}
}

func deleteBuilderCommentHandler(w http.ResponseWriter, r *http.Request) {
	defer timer.New("deleteBuilderCommentHandler").Stop()
	if !userHasEditRights(r) {
		util.ReportError(w, r, fmt.Errorf("User does not have edit rights."), "User does not have edit rights.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	cache, err := getCommitCache(w, r)
	if err != nil {
		return
	}
	builder := mux.Vars(r)["builder"]
	commentId, err := strconv.ParseInt(mux.Vars(r)["commentId"], 10, 32)
	if err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Invalid comment id: %v", err))
		return
	}
	if err := cache.DeleteBuilderComment(builder, int(commentId)); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to delete comment: %v", err))
		return
	}
}

func addCommitCommentHandler(w http.ResponseWriter, r *http.Request) {
	defer timer.New("addCommitCommentHandler").Stop()
	if !userHasEditRights(r) {
		util.ReportError(w, r, fmt.Errorf("User does not have edit rights."), "User does not have edit rights.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	commit := mux.Vars(r)["commit"]
	comment := struct {
		Comment string `json:"comment"`
	}{}
	if err := json.NewDecoder(r.Body).Decode(&comment); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to add comment: %v", err))
		return
	}
	defer util.Close(r.Body)

	c := buildbot.CommitComment{
		Commit:    commit,
		User:      login.LoggedInAs(r),
		Timestamp: float64(time.Now().UTC().Unix()),
		Message:   comment.Comment,
	}
	if _, err := c.InsertIntoDB(); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to add commit comment: %v", err))
		return
	}
}

func deleteCommitCommentHandler(w http.ResponseWriter, r *http.Request) {
	defer timer.New("deleteCommitCommentHandler").Stop()
	if !userHasEditRights(r) {
		util.ReportError(w, r, fmt.Errorf("User does not have edit rights."), "User does not have edit rights.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	commentId, err := strconv.ParseInt(mux.Vars(r)["commentId"], 10, 32)
	if err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Invalid comment id: %v", err))
		return
	}
	if err := buildbot.DeleteCommitComment(int(commentId)); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to delete commit comment: %v", err))
		return
	}
}

func commitsHandler(w http.ResponseWriter, r *http.Request) {
	defer timer.New("commitsHandler").Stop()
	w.Header().Set("Content-Type", "text/html")

	// Don't use cached templates in testing mode.
	if *testing {
		reloadTemplates()
	}

	if err := commitsTemplate.Execute(w, struct{}{}); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to expand template: %v", err))
	}
}

func infraHandler(w http.ResponseWriter, r *http.Request) {
	defer timer.New("infraHandler").Stop()
	w.Header().Set("Content-Type", "text/html")

	// Don't use cached templates in testing mode.
	if *testing {
		reloadTemplates()
	}

	if err := infraTemplate.Execute(w, struct{}{}); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to expand template: %v", err))
	}
}

func buildsJsonHandler(w http.ResponseWriter, r *http.Request) {
	defer timer.New("buildsHandler").Stop()
	w.Header().Set("Content-Type", "application/json")

	start, err := getIntParam("start", r)
	if err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Invalid value for parameter \"start\": %v", err))
		return
	}
	end, err := getIntParam("end", r)
	if err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Invalid value for parameter \"end\": %v", err))
		return
	}

	var startTime time.Time
	var endTime time.Time

	if end == nil {
		endTime = time.Now()
	} else {
		endTime = time.Unix(int64(*end), 0)
	}

	if start == nil {
		startTime = endTime.AddDate(0, 0, -1)
	} else {
		startTime = time.Unix(int64(*start), 0)
	}

	// Fetch the builds.
	builds, err := buildbot.GetBuildsFromDateRange(startTime, endTime)
	if err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to load builds: %v", err))
		return
	}
	// Shrink the builds.
	// TODO(borenet): Can we share build-shrinking code with the main status
	// page?

	// TinyBuildStep is a struct containing a small subset of a BuildStep's fields.
	type TinyBuildStep struct {
		Name     string
		Started  float64
		Finished float64
		Results  int
	}

	// TinyBuild is a struct containing a small subset of a Build's fields.
	type TinyBuild struct {
		Builder    string
		BuildSlave string
		Master     string
		Number     int
		Properties [][]interface{} `json:"properties"`
		Started    float64
		Finished   float64
		Results    int
		Steps      []*TinyBuildStep
	}

	rv := make([]*TinyBuild, 0, len(builds))
	for _, b := range builds {
		steps := make([]*TinyBuildStep, 0, len(b.Steps))
		for _, s := range b.Steps {
			steps = append(steps, &TinyBuildStep{
				Name:     s.Name,
				Started:  s.Started,
				Finished: s.Finished,
				Results:  s.Results,
			})
		}
		rv = append(rv, &TinyBuild{
			Builder:    b.Builder,
			BuildSlave: b.BuildSlave,
			Master:     b.Master,
			Number:     b.Number,
			Properties: b.Properties,
			Started:    b.Started,
			Finished:   b.Finished,
			Results:    b.Results,
			Steps:      steps,
		})
	}

	defer timer.New("buildsHandler_encode").Stop()
	if err := json.NewEncoder(w).Encode(rv); err != nil {
		glog.Errorf("Failed to write or encode output: %s", err)
		return
	}
}

func buildbotDashHandler(w http.ResponseWriter, r *http.Request) {
	defer timer.New("buildbotDashHandler").Stop()
	w.Header().Set("Content-Type", "text/html")

	// Don't use cached templates in testing mode.
	if *testing {
		reloadTemplates()
	}

	if err := buildbotDashTemplate.Execute(w, struct{}{}); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to expand template: %v", err))
		return
	}
}

func perfJsonHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{"alerts": perfStatus}); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to report Perf status: %v", err))
		return
	}
}

func goldJsonHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"gm": goldGMStatus,
		// Uncomment once we track SKP's again.
		// "skp":   goldSKPStatus,
		"image": goldImageStatus,
	}); err != nil {
		glog.Errorf("Failed to write or encode output: %s", err)
		return
	}
}

func autoRollJsonHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(rollStatus); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to report AutoRoll status: %v", err))
		return
	}
}

func slaveHostsJsonHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"hosts":          slaveHosts,
		"androidDevices": androidDevices,
		"sshDevices":     sshDevices,
	}); err != nil {
		glog.Errorf("Failed to write or encode output: %s", err)
		return
	}
}

func hostsHandler(w http.ResponseWriter, r *http.Request) {
	defer timer.New("hostsHandler").Stop()
	w.Header().Set("Content-Type", "text/html")

	// Don't use cached templates in testing mode.
	if *testing {
		reloadTemplates()
	}

	if err := hostsTemplate.Execute(w, struct{}{}); err != nil {
		glog.Errorf("Failed to write or encode output: %s", err)
	}
}

// buildProgressHandler returns the number of finished builds at the given
// commit, compared to that of an older commit.
func buildProgressHandler(w http.ResponseWriter, r *http.Request) {
	defer timer.New("buildProgressHandler").Stop()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Get the commit cache.
	cache, err := getCommitCache(w, r)
	if err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to get the commit cache."))
		return
	}

	// Get the number of finished builds for the requested commit.
	hash := r.FormValue("commit")
	buildsAtNewCommit, err := cache.GetBuildsForCommit(hash)
	if err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to get the number of finished builds."))
		return
	}
	finishedAtNewCommit := 0
	for _, b := range buildsAtNewCommit {
		if b.Finished {
			finishedAtNewCommit++
		}
	}

	// Find an older commit for which we'll assume that all builds have completed.
	oldCommit, err := cache.Get(cache.NumCommits() - DEFAULT_COMMITS_TO_LOAD)
	if err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to get an old commit from the cache."))
		return
	}
	buildsAtOldCommit, err := cache.GetBuildsForCommit(oldCommit.Hash)
	if err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to get the number of finished builds."))
		return
	}
	finishedAtOldCommit := 0
	for _, b := range buildsAtOldCommit {
		if b.Finished {
			finishedAtOldCommit++
		}
	}
	res := struct {
		OldCommit           string  `json:"oldCommit"`
		FinishedAtOldCommit int     `json:"finishedAtOldCommit"`
		NewCommit           string  `json:"newCommit"`
		FinishedAtNewCommit int     `json:"finishedAtNewCommit"`
		FinishedProportion  float64 `json:"finishedProportion"`
	}{
		OldCommit:           oldCommit.Hash,
		FinishedAtOldCommit: finishedAtOldCommit,
		NewCommit:           hash,
		FinishedAtNewCommit: finishedAtNewCommit,
		FinishedProportion:  float64(finishedAtNewCommit) / float64(finishedAtOldCommit),
	}
	if err := json.NewEncoder(w).Encode(res); err != nil {
		util.ReportError(w, r, err, fmt.Sprintf("Failed to encode JSON."))
		return
	}
}

func runServer(serverURL string) {
	r := mux.NewRouter()
	r.HandleFunc("/", commitsHandler)
	r.HandleFunc("/buildbots", buildbotDashHandler)
	r.HandleFunc("/hosts", hostsHandler)
	r.HandleFunc("/infra", infraHandler)
	r.HandleFunc("/json/autoRoll", autoRollJsonHandler)
	r.HandleFunc("/json/builds", buildsJsonHandler)
	r.HandleFunc("/json/goldStatus", goldJsonHandler)
	r.HandleFunc("/json/perfAlerts", perfJsonHandler)
	r.HandleFunc("/json/slaveHosts", slaveHostsJsonHandler)
	r.HandleFunc("/json/version", skiaversion.JsonHandler)
	r.HandleFunc("/json/{repo}/buildProgress", buildProgressHandler)
	r.HandleFunc("/logout/", login.LogoutHandler)
	r.HandleFunc("/loginstatus/", login.StatusHandler)
	r.HandleFunc("/oauth2callback/", login.OAuth2CallbackHandler)
	r.PathPrefix("/res/").HandlerFunc(util.MakeResourceHandler(*resourcesDir))
	builds := r.PathPrefix("/json/{repo}/builds/{buildId:[0-9]+}").Subrouter()
	builds.HandleFunc("/comments", addBuildCommentHandler).Methods("POST")
	builds.HandleFunc("/comments/{commentId:[0-9]+}", deleteBuildCommentHandler).Methods("DELETE")
	builders := r.PathPrefix("/json/{repo}/builders/{builder}").Subrouter()
	builders.HandleFunc("/comments", addBuilderCommentHandler).Methods("POST")
	builders.HandleFunc("/comments/{commentId:[0-9]+}", deleteBuilderCommentHandler).Methods("DELETE")
	commits := r.PathPrefix("/json/{repo}/commits").Subrouter()
	commits.HandleFunc("/", commitsJsonHandler)
	commits.HandleFunc("/{commit:[a-f0-9]+}/comments", addCommitCommentHandler).Methods("POST")
	commits.HandleFunc("/{commit:[a-f0-9]+}/comments/{commentId:[0-9]+}", deleteCommitCommentHandler).Methods("DELETE")
	http.Handle("/", util.LoggingGzipRequestResponse(r))
	glog.Infof("Ready to serve on %s", serverURL)
	glog.Fatal(http.ListenAndServe(*port, nil))
}

func main() {
	defer common.LogPanic()
	// Setup flags.
	dbConf := buildbot.DBConfigFromFlags()
	influxdb.SetupFlags()

	common.InitWithMetrics("status", graphiteServer)
	v, err := skiaversion.GetVersion()
	if err != nil {
		glog.Fatal(err)
	}
	glog.Infof("Version %s, built at %s", v.Commit, v.Date)

	Init()
	if *testing {
		*useMetadata = false
	}
	serverURL := "https://" + *host
	if *testing {
		serverURL = "http://" + *host + *port
	}

	// Setup InfluxDB client.
	dbClient, err = influxdb.NewClientFromFlagsAndMetadata(*testing)
	if err != nil {
		glog.Fatal(err)
	}

	// By default use a set of credentials setup for localhost access.
	var cookieSalt = "notverysecret"
	var clientID = "31977622648-1873k0c1e5edaka4adpv1ppvhr5id3qm.apps.googleusercontent.com"
	var clientSecret = "cw0IosPu4yjaG2KWmppj2guj"
	var redirectURL = serverURL + "/oauth2callback/"
	if *useMetadata {
		cookieSalt = metadata.Must(metadata.ProjectGet(metadata.COOKIESALT))
		clientID = metadata.Must(metadata.ProjectGet(metadata.CLIENT_ID))
		clientSecret = metadata.Must(metadata.ProjectGet(metadata.CLIENT_SECRET))
	}
	login.Init(clientID, clientSecret, redirectURL, cookieSalt, login.DEFAULT_SCOPE, login.DEFAULT_DOMAIN_WHITELIST, false)

	// Check out source code.
	skiaRepo, err := gitinfo.CloneOrUpdate("https://skia.googlesource.com/skia.git", path.Join(*workdir, "skia"), true)
	if err != nil {
		glog.Fatalf("Failed to check out Skia: %v", err)
	}

	infraRepoPath := path.Join(*workdir, "infra")
	infraRepo, err := gitinfo.CloneOrUpdate("https://skia.googlesource.com/buildbot.git", infraRepoPath, true)
	if err != nil {
		glog.Fatalf("Failed to checkout Infra: %v", err)
	}

	glog.Info("CloneOrUpdate complete")

	// Initialize the buildbot database.
	if *useMetadata {
		if err := dbConf.GetPasswordFromMetadata(); err != nil {
			glog.Fatal(err)
		}
	}
	if err := dbConf.InitDB(); err != nil {
		glog.Fatal(err)
	}

	// Create the commit caches.
	commitCaches = map[string]*commit_cache.CommitCache{}
	skiaCache, err := commit_cache.New(skiaRepo, path.Join(*workdir, "commit_cache.gob"), DEFAULT_COMMITS_TO_LOAD)
	if err != nil {
		glog.Fatalf("Failed to create commit cache: %v", err)
	}
	commitCaches[SKIA_REPO] = skiaCache

	infraCache, err := commit_cache.New(infraRepo, path.Join(*workdir, "commit_cache_infra.gob"), DEFAULT_COMMITS_TO_LOAD)
	if err != nil {
		glog.Fatalf("Failed to create commit cache: %v", err)
	}
	commitCaches[INFRA_REPO] = infraCache
	glog.Info("commit_cache complete")

	// Load Perf and Gold data in a loop.
	perfStatus, err = dbClient.Int64PollingStatus("select value from \"alerting.new.value\" where app='skiaperf' and host='skia-perf' order by time desc limit 1", time.Minute)
	if err != nil {
		glog.Fatalf("Failed to create polling Perf status: %v", err)
	}
	goldGMStatus, err = dbClient.Int64PollingStatus(fmt.Sprintf(GOLD_STATUS_QUERY_TMPL, "gm"), time.Minute)
	if err != nil {
		glog.Fatalf("Failed to create polling Gold status: %v", err)
	}
	goldImageStatus, err = dbClient.Int64PollingStatus(fmt.Sprintf(GOLD_STATUS_QUERY_TMPL, "image"), time.Minute)
	if err != nil {
		glog.Fatalf("Failed to create polling Gold status: %v", err)
	}

	// Load slave_hosts_cfg and device cfgs in a loop.
	slaveHosts, err = buildbot.SlaveHostsCfgPoller(infraRepoPath)
	if err != nil {
		glog.Fatal(err)
	}
	androidDevices, err = device_cfg.AndroidDeviceCfgPoller(*workdir)
	if err != nil {
		glog.Fatal(err)
	}
	sshDevices, err = device_cfg.SSHDeviceCfgPoller(*workdir)
	if err != nil {
		glog.Fatal(err)
	}

	// Load AutoRoll data in a loop.
	rollStatus, err = autoroll.AutoRollStatusPoller(rietveld.New(autoroll.RIETVELD_URL, nil))
	if err != nil {
		glog.Fatal(err)
	}

	runServer(serverURL)
}
