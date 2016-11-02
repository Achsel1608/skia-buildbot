// shortcut handles storing and retrieving shortcuts.
package shortcut2

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"

	"go.skia.org/infra/perf/go/db"
)

type Shortcut struct {
	Keys []string `json:"keys"`
}

// Insert adds the shortcut content into the database. The id of the shortcut
// is returned.
func Insert(r io.Reader) (string, error) {
	b, err := ioutil.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("Unable to read shortcut body: %s", err)
	}
	result, err := db.DB.Exec(`INSERT INTO shortcuts (traces) VALUES (?)`, string(b))
	if err != nil {
		return "", fmt.Errorf("Error while inserting shortcut: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("Error retrieving ID of new shortcut: %v", err)
	}
	return fmt.Sprintf("%d", id), nil
}

// Get retrieves a parsed shortcut for the given id.
func Get(id string) (*Shortcut, error) {
	var s string
	if err := db.DB.QueryRow(`SELECT traces FROM shortcuts WHERE id =?`, id).Scan(&s); err != nil {
		return nil, fmt.Errorf("Error retrieving shortcut from db: %s", err)
	}
	ret := &Shortcut{}
	if err := json.Unmarshal([]byte(s), ret); err != nil {
		return nil, fmt.Errorf("Error decoding shortcut: %s", err)
	}
	return ret, nil
}
