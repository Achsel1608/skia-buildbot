/*
	Automatic DEPS rolls of Skia into Chrome.
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
	"text/template"
	"time"

	"github.com/gorilla/mux"
	"github.com/skia-dev/glog"

	"go.skia.org/infra/autoroll/go/autoroller"
	"go.skia.org/infra/go/auth"
	"go.skia.org/infra/go/common"
	"go.skia.org/infra/go/login"
	"go.skia.org/infra/go/metadata"
	"go.skia.org/infra/go/rietveld"
	"go.skia.org/infra/go/skiaversion"
	"go.skia.org/infra/go/util"
)

const (
	RIETVELD_URL = "https://codereview.chromium.org"
)

var (
	arb *autoroller.AutoRoller = nil

	mainTemplate *template.Template = nil
)

// flags
var (
	graphiteServer = flag.String("graphite_server", "localhost:2003", "Where is Graphite metrics ingestion server running.")
	host           = flag.String("host", "localhost", "HTTP service host")
	port           = flag.String("port", ":8000", "HTTP service port (e.g., ':8000')")
	useMetadata    = flag.Bool("use_metadata", true, "Load sensitive values from metadata not from flags.")
	testing        = flag.Bool("testing", false, "Set to true for locally testing rules. No email will be sent.")
	workdir        = flag.String("workdir", ".", "Directory to use for scratch work.")
	childName      = flag.String("childName", "Skia", "Name of the project to roll.")
	childPath      = flag.String("childPath", "src/third_party/skia", "Path within Chromium repo of the project to roll.")
	resourcesDir   = flag.String("resources_dir", "", "The directory to find templates, JS, and CSS files. If blank the current directory will be used.")
	depot_tools    = flag.String("depot_tools", "", "Path to the depot_tools installation. If empty, assumes depot_tools is in PATH.")
)

func getSheriff() ([]string, error) {
	resp, err := http.Get("https://skia-tree-status.appspot.com/current-sheriff")
	if err != nil {
		return nil, err
	}
	defer util.Close(resp.Body)
	var sheriff struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sheriff); err != nil {
		return nil, err
	}
	return []string{sheriff.Username}, nil
}

func getCQExtraTrybots() ([]string, error) {
	return []string{"tryserver.blink:linux_blink_rel"}, nil
}

func reloadTemplates() {
	// Change the current working directory to two directories up from this source file so that we
	// can read templates and serve static (res/) files.

	if *resourcesDir == "" {
		_, filename, _, _ := runtime.Caller(0)
		*resourcesDir = filepath.Join(filepath.Dir(filename), "../..")
	}
	mainTemplate = template.Must(template.ParseFiles(
		filepath.Join(*resourcesDir, "templates/main.html"),
		filepath.Join(*resourcesDir, "templates/header.html"),
	))
}

func Init() {
	reloadTemplates()
}

func modeJsonHandler(w http.ResponseWriter, r *http.Request) {
	if !login.IsAGoogler(r) {
		util.ReportError(w, r, fmt.Errorf("User does not have edit rights."), "You must be logged in with an @google.com account to do that.")
		return
	}

	var mode struct {
		Mode string `json:"mode"`
	}
	defer util.Close(r.Body)
	if err := json.NewDecoder(r.Body).Decode(&mode); err != nil {
		util.ReportError(w, r, err, "Failed to decode request body.")
		return
	}

	if err := arb.SetMode(mode.Mode, login.LoggedInAs(r), "[Placeholder Message]"); err != nil {
		util.ReportError(w, r, err, "Failed to set AutoRoll mode.")
		return
	}

	// Return the ARB status.
	statusJsonHandler(w, r)
}

func statusJsonHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Obtain the status info. Only display error messages if the user
	// is a logged-in Googler.
	status := arb.GetStatus(login.IsAGoogler(r))
	if err := json.NewEncoder(w).Encode(&status); err != nil {
		glog.Error(err)
	}
}

func mainHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")

	// Don't use cached templates in testing mode.
	if *testing {
		reloadTemplates()
	}
	mainPage := struct {
		ProjectName string
		ProjectUser string
	}{
		ProjectName: *childName,
		ProjectUser: arb.User(),
	}
	if err := mainTemplate.Execute(w, mainPage); err != nil {
		glog.Errorln("Failed to expand template:", err)
	}
}

func runServer(serverURL string) {
	r := mux.NewRouter()
	r.PathPrefix("/res/").HandlerFunc(util.MakeResourceHandler(*resourcesDir))
	r.HandleFunc("/", mainHandler)
	r.HandleFunc("/json/mode", modeJsonHandler).Methods("POST")
	r.HandleFunc("/json/status", util.CorsHandler(statusJsonHandler))
	r.HandleFunc("/json/version", skiaversion.JsonHandler)
	r.HandleFunc("/oauth2callback/", login.OAuth2CallbackHandler)
	r.HandleFunc("/logout/", login.LogoutHandler)
	r.HandleFunc("/loginstatus/", login.StatusHandler)
	http.Handle("/", util.LoggingGzipRequestResponse(r))
	glog.Infof("Ready to serve on %s", serverURL)
	glog.Fatal(http.ListenAndServe(*port, nil))
}

func main() {
	defer common.LogPanic()
	common.InitWithMetrics("autoroll", graphiteServer)
	Init()

	v, err := skiaversion.GetVersion()
	if err != nil {
		glog.Fatal(err)
	}
	glog.Infof("Version %s, built at %s", v.Commit, v.Date)

	if *testing {
		*useMetadata = false
	}

	// Create the Rietveld client.
	client, err := auth.NewClientFromIdAndSecret(rietveld.CLIENT_ID, rietveld.CLIENT_SECRET, path.Join(*workdir, "oauth_cache"), rietveld.OAUTH_SCOPES...)
	if err != nil {
		glog.Fatal(err)
	}
	r := rietveld.New(RIETVELD_URL, client)

	// Retrieve the list of extra CQ trybots.
	// TODO(borenet): Make this editable on the web front-end.
	cqExtraTrybots, err := getCQExtraTrybots()
	if err != nil {
		glog.Fatal(err)
	}

	// Retrieve the initial email list.
	emails, err := getSheriff()
	if err != nil {
		glog.Fatal(err)
	}

	// Start the autoroller.
	arb, err = autoroller.NewAutoRoller(*workdir, *childPath, cqExtraTrybots, emails, r, time.Minute, 15*time.Minute, *depot_tools)
	if err != nil {
		glog.Fatal(err)
	}

	// Update the current sheriff in a loop.
	go func() {
		for _ = range time.Tick(30 * time.Minute) {
			emails, err := getSheriff()
			if err != nil {
				glog.Errorf("Failed to retrieve current sheriff: %s", err)
			} else {
				arb.SetEmails(emails)
			}
		}
	}()

	serverURL := "https://" + *host
	if *testing {
		serverURL = "http://" + *host + *port
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

	runServer(serverURL)
}
