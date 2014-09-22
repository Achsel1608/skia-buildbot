package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/golang/glog"
	_ "github.com/mattn/go-sqlite3"
	"skia.googlesource.com/buildbot.git/perf/go/metadata"
)

var (
	// DB is the sql database where we have commit and annotation information stored.
	DB *sql.DB = nil
)

const (
	METADATA_KEY = "readwrite"
)

// Init must be called once before DB is used.
//
// Since it used glog, make sure it is also called after flag.Parse is called.
func Init() {
	// Connect to MySQL server. First, get the password from the metadata server.
	// See https://developers.google.com/compute/docs/metadata#custom.
	password, err := metadata.Get(METADATA_KEY)
	if err == nil {
		// The IP address of the database is found here:
		//    https://console.developers.google.com/project/31977622648/sql/instances/skiaperf/overview
		// And 3306 is the default port for MySQL.
		DB, err = sql.Open("mysql", fmt.Sprintf("readwrite:%s@tcp(173.194.104.24:3306)/skia?parseTime=true", password))
		if err != nil {
			glog.Fatalln("Failed to open connection to SQL server:", err)
		}
	} else {
		glog.Infoln("Failed to find metadata, unable to connect to MySQL server (Expected when running locally):", err)
		// Fallback to sqlite for local use.
		DB, err = sql.Open("sqlite3", "./perf.db")
		if err != nil {
			glog.Fatalln("Failed to open:", err)
		}

		sql := `CREATE TABLE clusters (
      id         INTEGER      NOT NULL PRIMARY KEY AUTOINCREMENT,
      ts         TIMESTAMP    NOT NULL,
      hash       TEXT         NOT NULL,
      regression FLOAT        NOT NULL,
      cluster    MEDIUMTEXT   NOT NULL,
      status     TEXT         NOT NULL,
      message    TEXT         NOT NULL
    )`
		_, err = DB.Exec(sql)
		glog.Infoln("Status creating sqlite table for clusters:", err)

		sql = `CREATE TABLE shortcuts (
      id      INTEGER     NOT NULL PRIMARY KEY AUTOINCREMENT,
      traces  MEDIUMTEXT  NOT NULL
    )`
		_, err = DB.Exec(sql)
		glog.Infoln("Status creating sqlite table for shortcuts:", err)

		sql = `CREATE TABLE tries (
      issue       VARCHAR(255) NOT NULL PRIMARY KEY,
      lastUpdated TIMESTAMP    NOT NULL,
      results     MEDIUMTEXT   NOT NULL
    )`
		_, err = DB.Exec(sql)
		glog.Infoln("Status creating sqlite table for tries:", err)
	}

	// Ping the database to keep the connection fresh.
	go func() {
		c := time.Tick(1 * time.Minute)
		for _ = range c {
			if err := DB.Ping(); err != nil {
				glog.Warningln("Database failed to respond:", err)
			}
			glog.Infof("db: Successful ping")
		}
	}()
}
