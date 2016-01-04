package main

// Executes database migrations to the latest target version. In production this
// requires the root password for MySQL. The user will be prompted for that so
// it is not entered via the command line.

import (
	"flag"

	"github.com/skia-dev/glog"
	"go.skia.org/infra/go/buildbot_deprecated"
	"go.skia.org/infra/go/common"
	"go.skia.org/infra/go/database"
)

var (
	local = flag.Bool("local", false, "Running locally if true. As opposed to in production.")
)

func main() {
	defer common.LogPanic()
	// Set up flags.
	dbConf := database.ConfigFromFlags(buildbot_deprecated.PROD_DB_HOST, buildbot_deprecated.PROD_DB_PORT, database.USER_ROOT, buildbot_deprecated.PROD_DB_NAME, buildbot_deprecated.MigrationSteps())

	// Global init to initialize glog and parse arguments.
	common.Init()

	if err := dbConf.PromptForPassword(); err != nil {
		glog.Fatal(err)
	}
	vdb, err := dbConf.NewVersionedDB()
	if err != nil {
		glog.Fatal(err)
	}

	// Get the current database version
	maxDBVersion := vdb.MaxDBVersion()
	glog.Infof("Latest database version: %d", maxDBVersion)

	dbVersion, err := vdb.DBVersion()
	if err != nil {
		glog.Fatalf("Unable to retrieve database version. Error: %s", err)
	}
	glog.Infof("Current database version: %d", dbVersion)

	if dbVersion < maxDBVersion {
		glog.Infof("Migrating to version: %d", maxDBVersion)
		err = vdb.Migrate(maxDBVersion)
		if err != nil {
			glog.Fatalf("Unable to retrieve database version. Error: %s", err)
		}
	}

	glog.Infoln("Database migration finished.")
}
