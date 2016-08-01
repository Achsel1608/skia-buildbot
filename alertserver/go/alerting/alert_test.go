package alerting

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	assert "github.com/stretchr/testify/require"
	"go.skia.org/infra/go/database/testutil"
	"go.skia.org/infra/go/testutils"
)

// makeAlert returns an example Alert.
func makeAlert() *Alert {
	now := time.Now().UTC().Unix()
	return &Alert{
		Id:           9,
		Name:         "My Dummy Alert",
		Category:     "testing",
		Triggered:    now - 10000,
		SnoozedUntil: now - 5000,
		DismissedAt:  0,
		Message:      "This is a test!",
		Nag:          int64(time.Hour),
		AutoDismiss:  0,
		LastFired:    now,
		Comments: []*Comment{
			&Comment{
				User:    "me",
				Time:    now - 7000,
				Message: "Wow, look at this alert!",
			},
			&Comment{
				User:    "you",
				Time:    now - 6000,
				Message: "yeah, it's pretty awesome.",
			},
		},
		Actions: []Action{NewPrintAction()},
	}
}

// clearDB initializes the database, upgrading it if needed, and removes all
// data to ensure that the test begins with a clean slate. Returns a MySQLTestDatabase
// which must be closed after the test finishes.
func clearDB(t *testing.T) *testutil.MySQLTestDatabase {
	failMsg := "Database initialization failed. Do you have the test database set up properly?  Details: %v"

	// Set up the database.
	testDb := testutil.SetupMySQLTestDatabase(t, migrationSteps)

	conf := testutil.LocalTestDatabaseConfig(migrationSteps)
	var err error
	DB, err = sqlx.Open("mysql", conf.MySQLString())
	assert.NoError(t, err, failMsg)

	return testDb
}

// TestAlertJsonSerialization verifies that we properly serialize and
// deserialize Alerts to JSON.
func TestAlertJsonSerialization(t *testing.T) {
	cases := []*Alert{
		&Alert{Comments: []*Comment{}, Actions: []Action{}}, // Empty Alert.
		makeAlert(), // Realistic case.
	}

	for _, want := range cases {
		b, err := json.Marshal(want)
		assert.NoError(t, err)
		got := &Alert{}
		assert.NoError(t, json.Unmarshal(b, got))
		testutils.AssertDeepEqual(t, got, want)
	}
}

// TestAlertDBSerialization verifies that we properly serialize and
// deserialize Alerts into the DB.
func TestAlertDBSerialization(t *testing.T) {
	testutils.SkipIfShort(t)
	d := clearDB(t)
	defer d.Close(t)

	cases := []*Alert{
		&Alert{},    // Empty Alert.
		makeAlert(), // Realistic case.
	}

	for _, want := range cases {
		assert.NoError(t, want.retryReplaceIntoDB())
		a, err := GetActiveAlerts()
		assert.NoError(t, err)
		assert.Equal(t, 1, len(a))
		got := a[0]
		testutils.AssertDeepEqual(t, got, want)
		// Dismiss the Alert, so that it doesn't show up later.
		got.DismissedAt = 1000
		assert.NoError(t, got.retryReplaceIntoDB())
	}
}

// TestAlertFlowE2E verifies that we can insert an Alert, manipulate it,
// retrieve it, and dismiss it properly.
func TestAlertFlowE2E(t *testing.T) {
	testutils.SkipIfShort(t)
	d := clearDB(t)
	defer d.Close(t)

	am, err := MakeAlertManager(time.Millisecond, nil)
	assert.NoError(t, err)

	// Stop the AlertManager's polling loop so that we can trigger it
	// manually instead.
	am.Stop()

	// Insert an Alert.
	a := makeAlert()
	assert.NoError(t, am.AddAlert(a))

	// Verify that the Alert is active and not snoozed.
	assert.NoError(t, am.tick())
	getAlerts := func() []*Alert {
		b := bytes.NewBuffer([]byte{})
		assert.NoError(t, am.WriteActiveAlertsJson(b, func(*Alert) bool { return true }))
		var active []*Alert
		assert.NoError(t, json.Unmarshal(b.Bytes(), &active))
		return active
	}
	getAlert := func() *Alert {
		active := getAlerts()
		assert.Equal(t, 1, len(active))
		return active[0]
	}
	got := getAlert()
	assert.True(t, am.Contains(got.Id))
	assert.False(t, got.Snoozed())

	// Snooze the Alert.
	assert.NoError(t, am.Snooze(got.Id, time.Now().UTC().Add(30*time.Second), "test_user", "msg"))
	assert.True(t, getAlert().Snoozed())

	// Unsnooze the Alert.
	assert.NoError(t, am.Unsnooze(got.Id, "test_user", "msg"))
	assert.False(t, getAlert().Snoozed())

	// Snooze the Alert and make sure it gets dismissed after the snooze
	// period expires.
	assert.NoError(t, am.Snooze(got.Id, time.Now().UTC().Add(1*time.Millisecond), "test_user", "msg"))
	time.Sleep(2 * time.Second)
	assert.NoError(t, am.tick())
	assert.False(t, am.Contains(got.Id))
	assert.Equal(t, 0, len(getAlerts()))

	// Add another Alert. Dismiss it.
	assert.NoError(t, am.AddAlert(a))
	assert.NoError(t, am.Dismiss(getAlert().Id, "test_user", "test dismiss"))
	assert.Equal(t, 0, len(getAlerts()))

	// Ensure that Alerts auto-dismiss appropriately.
	a.AutoDismiss = int64(time.Second)
	assert.NoError(t, am.AddAlert(a))
	// Normally, the Alert would be re-firing during this time...
	time.Sleep(2 * time.Second)
	assert.NoError(t, am.tick())
	// But since it didn't, we expect no active alerts.
	assert.Equal(t, 0, len(getAlerts()))

	// Now, ensure that Alerts DON'T auto-dismiss when they shouldn't.
	a = makeAlert()
	assert.NoError(t, am.AddAlert(a))
	time.Sleep(2 * time.Second)
	assert.NoError(t, am.tick())
	assert.Equal(t, 1, len(getAlerts()))
}
