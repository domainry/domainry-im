package saas

import "os"

func ensureDirectory(path string) error { return os.MkdirAll(path, 0o750) }
