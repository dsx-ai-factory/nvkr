package vuln

import (
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func code(err error) nkderr.Code {
	if e, ok := err.(*nkderr.Error); ok {
		return e.Code
	}
	return ""
}
