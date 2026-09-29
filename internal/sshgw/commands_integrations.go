package sshgw

import (
	"fmt"
	"golang.org/x/term"

	"github.com/skrashevich/svkexe/internal/integrations"
)

func integrationCommand() *command {
	return &command{Name: "integration", Group: groupAccount, Usage: "integration [list|providers|add|rm]", Summary: "Connect external services for your VMs", Description: "Owner-scoped service credentials. Secrets are entered without echo in a terminal, or as JSON on stdin for a one-shot command. Saved secrets are never printed.", JSON: true, Run: cmdIntegrationList, Subcommands: []subSpec{
		{Name: "list", Usage: "integration list [--json]", Desc: "List configured connections without secrets", JSON: true, Run: cmdIntegrationList},
		{Name: "providers", Usage: "integration providers [--json]", Desc: "List supported providers and their input fields", JSON: true, Run: cmdIntegrationProviders},
		{Name: "add", Usage: "integration add <provider>", Desc: "Save or replace a connection; hidden prompts or JSON stdin", Args: []argSpec{{Name: "provider", Desc: "Provider identifier"}}, Run: cmdIntegrationAdd},
		{Name: "rm", Usage: "integration rm <provider>", Desc: "Stop publishing this connection to your VMs", Args: []argSpec{{Name: "provider", Desc: "Provider identifier"}}, Run: cmdIntegrationRemove},
	}}
}
func cmdIntegrationList(c *cmdCtx) error {
	list, err := c.s.serviceIntegrations().List(c.ctx, c.user.ID)
	if err != nil {
		return fmt.Errorf("could not list integrations")
	}
	if c.json {
		return c.writeJSON(list)
	}
	for _, connection := range list {
		c.printf("%s: connected\n", connection.Provider)
	}
	if len(list) == 0 {
		c.print("No integrations connected. Use integration providers and integration add <provider>.\n")
	}
	return nil
}
func cmdIntegrationProviders(c *cmdCtx) error {
	descriptors := c.s.serviceIntegrations().Descriptors()
	if c.json {
		return c.writeJSON(descriptors)
	}
	for _, d := range descriptors {
		c.printf("%s — %s\n", d.ID, d.Name)
		for _, f := range d.Config {
			c.printf("  config: %s (%s)\n", f.Name, f.Label)
		}
		for _, f := range d.Secrets {
			c.printf("  secret: %s (%s)\n", f.Name, f.Label)
		}
	}
	return nil
}
func cmdIntegrationAdd(c *cmdCtx) error {
	svc := c.s.serviceIntegrations()
	provider := c.arg(0)
	desc, err := svc.Descriptor(provider)
	if err != nil {
		return fmt.Errorf("unknown provider; use integration providers")
	}
	if c.sess == nil {
		return fmt.Errorf("credential input requires an SSH session")
	}
	var in integrations.Input
	if c.pty {
		terminal := term.NewTerminal(c.sess, "")
		in = integrations.Input{Config: map[string]string{}, Secrets: map[string]string{}}
		for _, f := range desc.Config {
			terminal.SetPrompt(f.Label + ": ")
			value, e := terminal.ReadLine()
			if e != nil {
				return fmt.Errorf("could not read configuration")
			}
			in.Config[f.Name] = value
		}
		for _, f := range desc.Secrets {
			value, e := terminal.ReadPassword(f.Label + ": ")
			if e != nil {
				return fmt.Errorf("could not read credentials")
			}
			in.Secrets[f.Name] = value
		}
	} else {
		in, err = integrations.DecodeInput(c.sess)
		if err != nil {
			return fmt.Errorf("send credential JSON on stdin: config and secrets objects")
		}
	}
	if err = svc.Save(c.ctx, c.user.ID, provider, in); err != nil {
		return fmt.Errorf("could not save integration; check required fields")
	}
	c.printf("Integration %s saved for your VMs.\n", provider)
	return nil
}
func cmdIntegrationRemove(c *cmdCtx) error {
	if err := c.s.serviceIntegrations().Delete(c.ctx, c.user.ID, c.arg(0)); err != nil {
		return fmt.Errorf("could not delete integration")
	}
	c.print("Integration disconnected. Revoke credentials at the external service to invalidate existing copies.\n")
	return nil
}
