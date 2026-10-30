package fixture

import (
	"fmt"
	"os"
)

func returnsError() error { return nil }
func check(f *os.File, ch chan error) {
	f.Close()
	fmt.Println("excluded")
	_ = returnsError()
	<-ch
	_ = <-ch
	x := <-ch
	_ = x
}
