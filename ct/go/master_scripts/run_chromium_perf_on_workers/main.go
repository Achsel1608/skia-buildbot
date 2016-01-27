// run_chromium_perf_on_workers is an application that runs the specified telemetry
// benchmark on all CT workers and uploads the results to Google Storage. The
// requester is emailed when the task is done.
package main

import (
	"bytes"
	"database/sql"
	"flag"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/skia-dev/glog"
	"go.skia.org/infra/ct/go/ctfe/chromium_perf"
	"go.skia.org/infra/ct/go/frontend"
	"go.skia.org/infra/ct/go/master_scripts/master_common"
	"go.skia.org/infra/ct/go/util"
	"go.skia.org/infra/go/common"
	"go.skia.org/infra/go/email"
	skutil "go.skia.org/infra/go/util"
)

var (
	emails                    = flag.String("emails", "", "The comma separated email addresses to notify when the task is picked up and completes.")
	description               = flag.String("description", "", "The description of the run as entered by the requester.")
	gaeTaskID                 = flag.Int64("gae_task_id", -1, "The key of the App Engine task. This task will be updated when the task is completed.")
	pagesetType               = flag.String("pageset_type", "", "The type of pagesets to use. Eg: 10k, Mobile10k, All.")
	benchmarkName             = flag.String("benchmark_name", "", "The telemetry benchmark to run on the workers.")
	benchmarkExtraArgs        = flag.String("benchmark_extra_args", "", "The extra arguments that are passed to the specified benchmark.")
	browserExtraArgsNoPatch   = flag.String("browser_extra_args_nopatch", "", "The extra arguments that are passed to the browser while running the benchmark for the nopatch case.")
	browserExtraArgsWithPatch = flag.String("browser_extra_args_withpatch", "", "The extra arguments that are passed to the browser while running the benchmark for the withpatch case.")
	repeatBenchmark           = flag.Int("repeat_benchmark", 1, "The number of times the benchmark should be repeated. For skpicture_printer benchmark this value is always 1.")
	runInParallel             = flag.Bool("run_in_parallel", false, "Run the benchmark by bringing up multiple chrome instances in parallel.")
	targetPlatform            = flag.String("target_platform", util.PLATFORM_ANDROID, "The platform the benchmark will run on (Android / Linux).")
	runID                     = flag.String("run_id", "", "The unique run id (typically requester + timestamp).")
	varianceThreshold         = flag.Float64("variance_threshold", 0.0, "The variance threshold to use when comparing the resultant CSV files.")
	discardOutliers           = flag.Float64("discard_outliers", 0.0, "The percentage of outliers to discard when comparing the result CSV files.")

	taskCompletedSuccessfully = false

	htmlOutputLink      = util.MASTER_LOGSERVER_LINK
	skiaPatchLink       = util.MASTER_LOGSERVER_LINK
	chromiumPatchLink   = util.MASTER_LOGSERVER_LINK
	benchmarkPatchLink  = util.MASTER_LOGSERVER_LINK
	noPatchOutputLink   = util.MASTER_LOGSERVER_LINK
	withPatchOutputLink = util.MASTER_LOGSERVER_LINK
)

func sendEmail(recipients []string) {
	// Send completion email.
	emailSubject := fmt.Sprintf("Cluster telemetry chromium perf task has completed (%s)", *runID)
	failureHtml := ""
	viewActionMarkup := ""
	var err error

	if taskCompletedSuccessfully {
		if viewActionMarkup, err = email.GetViewActionMarkup(htmlOutputLink, "View Results", "Direct link to the HTML results"); err != nil {
			glog.Errorf("Failed to get view action markup: %s", err)
			return
		}
	} else {
		emailSubject += " with failures"
		failureHtml = util.GetFailureEmailHtml(*runID)
		if viewActionMarkup, err = email.GetViewActionMarkup(util.GetMasterLogLink(*runID), "View Failure", "Direct link to the master log"); err != nil {
			glog.Errorf("Failed to get view action markup: %s", err)
			return
		}
	}
	bodyTemplate := `
	The chromium perf %s benchmark task on %s pageset has completed.<br/>
	Run description: %s<br/>
	%s
	The HTML output with differences between the base run and the patch run is <a href='%s'>here</a>.<br/>
	The patch(es) you specified are here:
	<a href='%s'>chromium</a>/<a href='%s'>skia</a>
	<br/><br/>
	You can schedule more runs <a href='%s'>here</a>.
	<br/><br/>
	Thanks!
	`
	emailBody := fmt.Sprintf(bodyTemplate, *benchmarkName, *pagesetType, *description, failureHtml, htmlOutputLink, chromiumPatchLink, skiaPatchLink, frontend.ChromiumPerfTasksWebapp)
	if err := util.SendEmailWithMarkup(recipients, emailSubject, emailBody, viewActionMarkup); err != nil {
		glog.Errorf("Error while sending email: %s", err)
		return
	}
}

func updateWebappTask() {
	vars := chromium_perf.UpdateVars{}
	vars.Id = *gaeTaskID
	vars.SetCompleted(taskCompletedSuccessfully)
	vars.Results = sql.NullString{String: htmlOutputLink, Valid: true}
	vars.NoPatchRawOutput = sql.NullString{String: noPatchOutputLink, Valid: true}
	vars.WithPatchRawOutput = sql.NullString{String: withPatchOutputLink, Valid: true}
	skutil.LogErr(frontend.UpdateWebappTaskV2(&vars))
}

func main() {
	defer common.LogPanic()
	master_common.Init()

	// Send start email.
	emailsArr := util.ParseEmails(*emails)
	emailsArr = append(emailsArr, util.CtAdmins...)
	if len(emailsArr) == 0 {
		glog.Error("At least one email address must be specified")
		return
	}
	skutil.LogErr(frontend.UpdateWebappTaskSetStarted(&chromium_perf.UpdateVars{}, *gaeTaskID))
	skutil.LogErr(util.SendTaskStartEmail(emailsArr, "Chromium perf", *runID, *description))
	// Ensure webapp is updated and email is sent even if task fails.
	defer updateWebappTask()
	defer sendEmail(emailsArr)
	// Cleanup dirs after run completes.
	defer skutil.RemoveAll(filepath.Join(util.StorageDir, util.ChromiumPerfRunsDir))
	defer skutil.RemoveAll(filepath.Join(util.StorageDir, util.BenchmarkRunsDir))
	if !*master_common.Local {
		// Cleanup tmp files after the run.
		defer util.CleanTmpDir()
	}
	// Finish with glog flush and how long the task took.
	defer util.TimeTrack(time.Now(), "Running chromium perf task on workers")
	defer glog.Flush()

	if *pagesetType == "" {
		glog.Error("Must specify --pageset_type")
		return
	}
	if *benchmarkName == "" {
		glog.Error("Must specify --benchmark_name")
		return
	}
	if *runID == "" {
		glog.Error("Must specify --run_id")
		return
	}

	// Instantiate GsUtil object.
	gs, err := util.NewGsUtil(nil)
	if err != nil {
		glog.Errorf("Could not instantiate gsutil object: %s", err)
		return
	}
	remoteOutputDir := filepath.Join(util.ChromiumPerfRunsDir, *runID)

	// Copy the patches to Google Storage.
	skiaPatchName := *runID + ".skia.patch"
	chromiumPatchName := *runID + ".chromium.patch"
	benchmarkPatchName := *runID + ".benchmark.patch"
	for _, patchName := range []string{skiaPatchName, chromiumPatchName, benchmarkPatchName} {
		if err := gs.UploadFile(patchName, os.TempDir(), remoteOutputDir); err != nil {
			glog.Errorf("Could not upload %s to %s: %s", patchName, remoteOutputDir, err)
			return
		}
	}
	skiaPatchLink = util.GS_HTTP_LINK + filepath.Join(util.GSBucketName, remoteOutputDir, skiaPatchName)
	chromiumPatchLink = util.GS_HTTP_LINK + filepath.Join(util.GSBucketName, remoteOutputDir, chromiumPatchName)
	benchmarkPatchLink = util.GS_HTTP_LINK + filepath.Join(util.GSBucketName, remoteOutputDir, benchmarkPatchName)

	// Create the two required chromium builds (with patch and without the patch).
	chromiumHash, skiaHash, err := util.CreateChromiumBuild(*runID, *targetPlatform, "", "", true)
	if err != nil {
		glog.Errorf("Could not create chromium build: %s", err)
		return
	}

	// Reboot all workers to start from a clean slate.
	if !*master_common.Local {
		util.RebootWorkers()
	}

	if *targetPlatform == util.PLATFORM_ANDROID {
		// Reboot all Android devices to start from a clean slate.
		util.RebootAndroidDevices()
	}

	// Run the run_chromium_perf script on all workers.
	runIDNoPatch := *runID + "-nopatch"
	runIDWithPatch := *runID + "-withpatch"
	chromiumBuildNoPatch := fmt.Sprintf("try-%s-%s-%s", chromiumHash, skiaHash, runIDNoPatch)
	chromiumBuildWithPatch := fmt.Sprintf("try-%s-%s-%s", chromiumHash, skiaHash, runIDWithPatch)
	runChromiumPerfCmdTemplate := "DISPLAY=:0 run_chromium_perf " +
		"--worker_num={{.WorkerNum}} --log_dir={{.LogDir}} --log_id={{.RunID}} --pageset_type={{.PagesetType}} " +
		"--chromium_build_nopatch={{.ChromiumBuildNoPatch}} --chromium_build_withpatch={{.ChromiumBuildWithPatch}} " +
		"--run_id={{.RunID}} --run_id_nopatch={{.RunIDNoPatch}} --run_id_withpatch={{.RunIDWithPatch}} " +
		"--benchmark_name={{.BenchmarkName}} --benchmark_extra_args=\"{{.BenchmarkExtraArgs}}\" " +
		"--browser_extra_args_nopatch=\"{{.BrowserExtraArgsNoPatch}}\" --browser_extra_args_withpatch=\"{{.BrowserExtraArgsWithPatch}}\" " +
		"--repeat_benchmark={{.RepeatBenchmark}} --run_in_parallel={{.RunInParallel}} --target_platform={{.TargetPlatform}} " +
		"--local={{.Local}};"
	runChromiumPerfTemplateParsed := template.Must(template.New("run_chromium_perf_cmd").Parse(runChromiumPerfCmdTemplate))
	runChromiumPerfCmdBytes := new(bytes.Buffer)
	if err := runChromiumPerfTemplateParsed.Execute(runChromiumPerfCmdBytes, struct {
		WorkerNum                 string
		LogDir                    string
		PagesetType               string
		ChromiumBuildNoPatch      string
		ChromiumBuildWithPatch    string
		RunID                     string
		RunIDNoPatch              string
		RunIDWithPatch            string
		BenchmarkName             string
		BenchmarkExtraArgs        string
		BrowserExtraArgsNoPatch   string
		BrowserExtraArgsWithPatch string
		RepeatBenchmark           int
		RunInParallel             bool
		TargetPlatform            string
		Local                     bool
	}{
		WorkerNum:              util.WORKER_NUM_KEYWORD,
		LogDir:                 util.GLogDir,
		PagesetType:            *pagesetType,
		ChromiumBuildNoPatch:   chromiumBuildNoPatch,
		ChromiumBuildWithPatch: chromiumBuildWithPatch,
		RunID:                     *runID,
		RunIDNoPatch:              runIDNoPatch,
		RunIDWithPatch:            runIDWithPatch,
		BenchmarkName:             *benchmarkName,
		BenchmarkExtraArgs:        *benchmarkExtraArgs,
		BrowserExtraArgsNoPatch:   *browserExtraArgsNoPatch,
		BrowserExtraArgsWithPatch: *browserExtraArgsWithPatch,
		RepeatBenchmark:           *repeatBenchmark,
		RunInParallel:             *runInParallel,
		TargetPlatform:            *targetPlatform,
		Local:                     *master_common.Local,
	}); err != nil {
		glog.Errorf("Failed to execute template: %s", err)
		return
	}
	cmd := append(master_common.WorkerSetupCmds(),
		// The main command that runs run_chromium_perf on all workers.
		runChromiumPerfCmdBytes.String())
	_, err = util.SSH(strings.Join(cmd, " "), util.Slaves, util.RUN_CHROMIUM_PERF_TIMEOUT)
	if err != nil {
		glog.Errorf("Error while running cmd %s: %s", cmd, err)
		return
	}

	// If "--output-format=csv-pivot-table" was specified then merge all CSV files and upload.
	noOutputSlaves := []string{}
	if strings.Contains(*benchmarkExtraArgs, "--output-format=csv-pivot-table") {
		for _, runID := range []string{runIDNoPatch, runIDWithPatch} {
			if noOutputSlaves, err = mergeUploadCSVFiles(runID, gs); err != nil {
				glog.Errorf("Unable to merge and upload CSV files for %s: %s", runID, err)
			}
		}
	}

	// Compare the resultant CSV files using csv_comparer.py
	noPatchCSVPath := filepath.Join(util.StorageDir, util.BenchmarkRunsDir, runIDNoPatch, runIDNoPatch+".output")
	withPatchCSVPath := filepath.Join(util.StorageDir, util.BenchmarkRunsDir, runIDWithPatch, runIDWithPatch+".output")
	htmlOutputDir := filepath.Join(util.StorageDir, util.ChromiumPerfRunsDir, *runID, "html")
	skutil.MkdirAll(htmlOutputDir, 0700)
	htmlRemoteDir := filepath.Join(remoteOutputDir, "html")
	htmlOutputLinkBase := util.GS_HTTP_LINK + filepath.Join(util.GSBucketName, htmlRemoteDir) + "/"
	htmlOutputLink = htmlOutputLinkBase + "index.html"
	noPatchOutputLink = util.GS_HTTP_LINK + filepath.Join(util.GSBucketName, util.BenchmarkRunsDir, runIDNoPatch, "consolidated_outputs", runIDNoPatch+".output")
	withPatchOutputLink = util.GS_HTTP_LINK + filepath.Join(util.GSBucketName, util.BenchmarkRunsDir, runIDWithPatch, "consolidated_outputs", runIDWithPatch+".output")
	// Construct path to the csv_comparer python script.
	_, currentFile, _, _ := runtime.Caller(0)
	pathToPyFiles := filepath.Join(
		filepath.Dir((filepath.Dir(filepath.Dir(filepath.Dir(currentFile))))),
		"py")
	pathToCsvComparer := filepath.Join(pathToPyFiles, "csv_comparer.py")
	args := []string{
		pathToCsvComparer,
		"--csv_file1=" + noPatchCSVPath,
		"--csv_file2=" + withPatchCSVPath,
		"--output_html=" + htmlOutputDir,
		"--variance_threshold=" + strconv.FormatFloat(*varianceThreshold, 'f', 2, 64),
		"--discard_outliers=" + strconv.FormatFloat(*discardOutliers, 'f', 2, 64),
		"--absolute_url=" + htmlOutputLinkBase,
		"--requester_email=" + *emails,
		"--skia_patch_link=" + skiaPatchLink,
		"--chromium_patch_link=" + chromiumPatchLink,
		"--benchmark_patch_link=" + benchmarkPatchLink,
		"--description=" + *description,
		"--raw_csv_nopatch=" + noPatchOutputLink,
		"--raw_csv_withpatch=" + withPatchOutputLink,
		"--num_repeated=" + strconv.Itoa(*repeatBenchmark),
		"--target_platform=" + *targetPlatform,
		"--browser_args_nopatch=" + *browserExtraArgsNoPatch,
		"--browser_args_withpatch=" + *browserExtraArgsWithPatch,
		"--pageset_type=" + *pagesetType,
		"--chromium_hash=" + chromiumHash,
		"--skia_hash=" + skiaHash,
		"--missing_output_slaves=" + strings.Join(noOutputSlaves, " "),
		"--logs_link_prefix=" + util.LOGS_LINK_PREFIX,
	}
	err = util.ExecuteCmd("python", args, []string{}, util.CSV_COMPARER_TIMEOUT, nil, nil)
	if err != nil {
		glog.Errorf("Error running csv_comparer.py: %s", err)
		return
	}

	// Copy the HTML files to Google Storage.
	if err := gs.UploadDir(htmlOutputDir, htmlRemoteDir, true); err != nil {
		glog.Errorf("Could not upload %s to %s: %s", htmlOutputDir, htmlRemoteDir, err)
		return
	}

	taskCompletedSuccessfully = true
}

func mergeUploadCSVFiles(runID string, gs *util.GsUtil) ([]string, error) {
	localOutputDir := filepath.Join(util.StorageDir, util.BenchmarkRunsDir, runID)
	skutil.MkdirAll(localOutputDir, 0700)
	noOutputSlaves := []string{}
	// Copy outputs from all slaves locally.
	for i := 0; i < util.NumWorkers(); i++ {
		workerNum := i + 1
		workerLocalOutputPath := filepath.Join(localOutputDir, fmt.Sprintf("slave%d", workerNum)+".csv")
		workerRemoteOutputPath := filepath.Join(util.BenchmarkRunsDir, runID, fmt.Sprintf("slave%d", workerNum), "outputs", runID+".output")
		respBody, err := gs.GetRemoteFileContents(workerRemoteOutputPath)
		if err != nil {
			glog.Errorf("Could not fetch %s: %s", workerRemoteOutputPath, err)
			noOutputSlaves = append(noOutputSlaves, fmt.Sprintf(util.WORKER_NAME_TEMPLATE, workerNum))
			continue
		}
		defer skutil.Close(respBody)
		out, err := os.Create(workerLocalOutputPath)
		if err != nil {
			return noOutputSlaves, fmt.Errorf("Unable to create file %s: %s", workerLocalOutputPath, err)
		}
		defer skutil.Close(out)
		defer skutil.Remove(workerLocalOutputPath)
		if _, err = io.Copy(out, respBody); err != nil {
			return noOutputSlaves, fmt.Errorf("Unable to copy to file %s: %s", workerLocalOutputPath, err)
		}
	}
	// Call csv_merger.py to merge all results into a single results CSV.
	_, currentFile, _, _ := runtime.Caller(0)
	pathToPyFiles := filepath.Join(
		filepath.Dir((filepath.Dir(filepath.Dir(filepath.Dir(currentFile))))),
		"py")
	pathToCsvMerger := filepath.Join(pathToPyFiles, "csv_merger.py")
	outputFileName := runID + ".output"
	args := []string{
		pathToCsvMerger,
		"--csv_dir=" + localOutputDir,
		"--output_csv_name=" + filepath.Join(localOutputDir, outputFileName),
	}
	err := util.ExecuteCmd("python", args, []string{}, util.CSV_MERGER_TIMEOUT, nil, nil)
	if err != nil {
		return noOutputSlaves, fmt.Errorf("Error running csv_merger.py: %s", err)
	}
	// Copy the output file to Google Storage.
	remoteOutputDir := filepath.Join(util.BenchmarkRunsDir, runID, "consolidated_outputs")
	if err := gs.UploadFile(outputFileName, localOutputDir, remoteOutputDir); err != nil {
		return noOutputSlaves, fmt.Errorf("Unable to upload %s to %s: %s", outputFileName, remoteOutputDir, err)
	}
	return noOutputSlaves, nil
}
