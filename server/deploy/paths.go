package deploy

import "path/filepath"

func filepathAbs(p string) (string, error) {
	return filepath.Abs(p)
}
