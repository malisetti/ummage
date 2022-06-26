package um

import (
	"bytes"
	"database/sql"
	"fmt"

	"golang.org/x/crypto/scrypt"
)

const (
	ImageFile = "image"
	AudioFile = "audio"
	VideoFile = "video"
)

type DeletionChoice int

const (
	_                           = iota
	DeleteOnTime DeletionChoice = iota + 1
	DeleteOnKeyIn
)

func (dc DeletionChoice) String() string {
	switch dc {
	case DeleteOnKeyIn:
		return "key-based"
	case DeleteOnTime:
		return "time-based"
	default:
		return "unknown-deletion-choice"
	}
}

type Resource struct {
	ID int

	UUID        string
	Name        string
	Caption     string
	ContentType string

	DestructKey     []byte // when this is entered we delete the img
	DestructKeySalt []byte // treat this as password
}

func (r *Resource) CanDelete(choice DeletionChoice, deletionPassword []byte) (bool, error) {
	switch choice {
	case DeleteOnKeyIn:
		dk, err := scrypt.Key(r.DestructKey, r.DestructKeySalt, 32768, 8, 1, 32)
		if err != nil {
			return false, fmt.Errorf("cryptographic function failed with %s", err.Error())
		}
		if bytes.Equal(dk, r.DestructKey) {
			return true, nil
		}
		return false, fmt.Errorf("can not delete the img using the given key")

	default:
		return false, fmt.Errorf("only one way exists to delete the resource")
	}
}

func (r *Resource) Add(tx *sql.Tx) (sql.Result, error) {
	stmt, err := tx.Prepare(`INSERT INTO resource(uuid, name, caption, content_type, destruct_key, destruct_key_salt) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	return stmt.Exec(r.UUID, r.Name, r.Caption, r.ContentType, r.DestructKey, r.DestructKeySalt)
}

func (r *Resource) Delete(tx *sql.Tx) (sql.Result, error) {
	stmt, err := tx.Prepare("DELETE FROM resource WHERE uuid = ?;")
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	return stmt.Exec(r.UUID)
}
