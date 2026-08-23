package main

import (
	"errors"
	"fmt"

	"github.com/urfave/cli/v2"
)

// execConnectionsRotateKey re-seals every stored credential under a fresh key.
//
// It goes through the running server rather than opening the database here: a
// separate process would rewrite every credential while the live one carried on
// holding the old key in memory, and every connection would break until the
// next restart.
func execConnectionsRotateKey(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 0 {
		return errors.New("usage: hostit control connections rotate-key")
	}
	n, err := cl.RotateConnectionKey()
	if err != nil {
		return err
	}
	fmt.Printf("Re-sealed %d credential(s) under a fresh key.\n", n)
	fmt.Println("The previous key is kept as connections.key.previous; delete it once you are sure.")
	return nil
}
