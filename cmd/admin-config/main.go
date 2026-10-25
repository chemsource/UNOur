// admin-config reads a password from stdin without putting it in argv or a file.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"unour/internal/admin"
)

func main() {
	path := flag.String("out", "", "new private JSON configuration path (must not exist)")
	flag.Parse()
	if *path == "" {
		fmt.Fprintln(os.Stderr, "usage: admin-config -out /private/admin.json < password-on-stdin")
		os.Exit(2)
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 513))
	if err == nil && len(data) > 512 {
		err = fmt.Errorf("password input is too long")
	}
	if err == nil {
		err = admin.WriteConfig(*path, string(data))
	}
	clear(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "Private admin configuration created. Password was not stored in plaintext.")
}
