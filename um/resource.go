package um

import (
	"bytes"
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
