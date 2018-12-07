package resource

import (
	"bytes"
	"fmt"

	"golang.org/x/crypto/scrypt"
)

const (
	imageFile = "image"
	audioFile = "audio"
	videoFile = "video"
)

type deletionChoice int

const (
	_                           = iota
	deleteOnTime deletionChoice = iota + 1
	deleteOnKeyIn
)

func (dc deletionChoice) String() string {
	switch dc {
	case deleteOnKeyIn:
		return "key-based"
	case deleteOnTime:
		return "time-based"
	default:
		return ""
	}
}

// Resource is bs
type Resource struct {
	ID int

	UUID        string
	Name        string
	Caption     string
	ContentType string

	DestructKey     []byte // when this is entered we delete the img
	DestructKeySalt []byte // treat this as password
}

func (r *Resource) canDelete(choice deletionChoice, deletionPassword []byte) (bool, error) {
	switch choice {
	case deleteOnKeyIn:
		dk, err := scrypt.Key(r.DestructKey, r.DestructKeySalt, 32768, 8, 1, 32)
		if err != nil {
			return false, fmt.Errorf("cryptographic function failed with %s", err.Error())
		}
		if bytes.Compare(dk, r.DestructKey) == 0 {
			return true, nil
		}
		return false, fmt.Errorf("can not delete the img using the given key")

	default:
		return false, fmt.Errorf("only one way exists to delete the resource")
	}
}
