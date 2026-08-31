package scim_test

import (
	"errors"
	"io"
	"net/http"

	"github.com/Helppi/helppi-scim-go/scim"
)

func asSCIM(err error, target **scim.Error) bool { return errors.As(err, target) }

func readAll(r *http.Request) ([]byte, error) { return io.ReadAll(r.Body) }
