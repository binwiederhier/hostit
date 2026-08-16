package main

import (
	"errors"
	"fmt"

	"github.com/urfave/cli/v2"
)

func execDomainList(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 1 {
		return errors.New("usage: hostit apps domain list <app>")
	}
	domains, err := cl.Domains(c.Args().First())
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		fmt.Println("No custom domains.")
		return nil
	}
	for _, d := range domains {
		fmt.Printf("%-40s %s\n", d.Domain, d.Status)
		if d.LastError != "" {
			fmt.Printf("  error: %s\n", d.LastError)
		}
	}
	return nil
}

func execDomainAdd(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 2 {
		return errors.New("usage: hostit apps domain add <app> <domain>")
	}
	d, err := cl.AddDomain(c.Args().First(), c.Args().Get(1))
	if err != nil {
		return err
	}
	fmt.Printf("Added %s (%s). Create these DNS records:\n\n", d.Domain, d.Status)
	for _, r := range d.DNS {
		fmt.Printf("  %-6s %s\n         -> %s\n         %s\n\n", r.Type, r.Name, r.Value, r.Note)
	}
	fmt.Println("Then run: hostit apps domain verify " + c.Args().First() + " " + d.Domain)
	return nil
}

func execDomainVerify(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 2 {
		return errors.New("usage: hostit apps domain verify <app> <domain>")
	}
	if err := cl.VerifyDomain(c.Args().First(), c.Args().Get(1)); err != nil {
		return err
	}
	fmt.Printf("Verifying %s; run 'domain list %s' to see the status.\n", c.Args().Get(1), c.Args().First())
	return nil
}

func execDomainRemove(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 2 {
		return errors.New("usage: hostit apps domain rm <app> <domain>")
	}
	if err := cl.DeleteDomain(c.Args().First(), c.Args().Get(1)); err != nil {
		return err
	}
	fmt.Printf("Removed %s from %s.\n", c.Args().Get(1), c.Args().First())
	return nil
}
