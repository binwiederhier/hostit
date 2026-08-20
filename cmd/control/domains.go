package main

import (
	"errors"
	"fmt"

	"github.com/urfave/cli/v2"

	"heckel.io/hostit/clitable"
)

func execDomainList(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 1 {
		return errors.New("usage: hostit control app domain list <app>")
	}
	domains, err := cl.Domains(c.Args().First())
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		fmt.Println("No custom domains.")
		return nil
	}
	rows := make([][]string, 0, len(domains))
	for _, d := range domains {
		rows = append(rows, []string{d.Domain, d.Status, d.LastError})
	}
	fmt.Println(clitable.Render([]string{"DOMAIN", "STATUS", "ERROR"}, rows))
	return nil
}

func execDomainAdd(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 2 {
		return errors.New("usage: hostit control app domain add <app> <domain>")
	}
	d, err := cl.AddDomain(c.Args().First(), c.Args().Get(1))
	if err != nil {
		return err
	}
	fmt.Printf("Added %s (%s). Create these DNS records:\n\n", d.Domain, d.Status)
	for _, r := range d.DNS {
		fmt.Printf("  %-6s %s\n         -> %s\n         %s\n\n", r.Type, r.Name, r.Value, r.Note)
	}
	fmt.Println("Then run: hostit control app domain verify " + c.Args().First() + " " + d.Domain)
	return nil
}

func execDomainVerify(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 2 {
		return errors.New("usage: hostit control app domain verify <app> <domain>")
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
		return errors.New("usage: hostit control app domain rm <app> <domain>")
	}
	if err := cl.DeleteDomain(c.Args().First(), c.Args().Get(1)); err != nil {
		return err
	}
	fmt.Printf("Removed %s from %s.\n", c.Args().Get(1), c.Args().First())
	return nil
}
