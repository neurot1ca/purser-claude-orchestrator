package nested

import "os"

func Check(f *os.File) { f.Close() }
