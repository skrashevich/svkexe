package sshgw

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/picoclaw"
	gossh "golang.org/x/crypto/ssh"
)

func accountCommands() []*command {
	return []*command{
		{
			Name:        "whoami",
			Group:       groupAccount,
			Usage:       "whoami [--json]",
			Summary:     "Show who you are and what this account holds",
			Description: "Reports the account this SSH key belongs to: address, role, age, how many VMs it owns, the model its VMs open on and the SSH keys that can reach it.",
			JSON:        true,
			Examples:    []string{"whoami", "whoami --json"},
			Run:         cmdWhoami,
		},
		{
			Name:        "ssh-key",
			Group:       groupAccount,
			Usage:       "ssh-key [list|add|remove] [arguments]",
			Summary:     "Manage the SSH keys that reach this account",
			Description: "Lists, registers and removes the public keys that authenticate you. The word alone lists them. A key is only ever shown by name and fingerprint; the gateway never prints a key back.",
			// The bare word lists, so it takes --json like "ssh-key list" does.
			// The writing actions declare neither, and the parser refuses the
			// flag there rather than accepting it and answering with prose.
			JSON: true,
			Subcommands: []subSpec{
				{
					Name:  "list",
					Usage: "ssh-key list [--json]",
					Desc:  "list your keys by name and fingerprint",
					JSON:  true,
					Run:   cmdSSHKeyList,
				},
				{
					Name:  "add",
					Usage: "ssh-key add <name> <public-key>",
					Desc:  "register an OpenSSH public key",
					Args: []argSpec{
						{Name: "name", Desc: "what to call the key"},
						// An OpenSSH public key is three space-separated fields,
						// so it arrives as several tokens unless the caller
						// quoted it; Repeated makes both spellings one argument.
						{Name: "public-key", Desc: "the whole OpenSSH public key line", Repeated: true},
					},
					Run: cmdSSHKeyAdd,
				},
				{
					Name:  "remove",
					Usage: "ssh-key remove <name>",
					Desc:  "remove the key with that name",
					Args:  []argSpec{{Name: "name", Desc: "the key to remove"}},
					Run:   cmdSSHKeyRemove,
				},
			},
			Examples: []string{
				"ssh-key list",
				"ssh-key add laptop ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... me@laptop",
				"ssh-key remove laptop",
			},
			Run: cmdSSHKeyList,
		},
		{
			Name:        "llm",
			Group:       groupAccount,
			Usage:       "llm [list|add|rm|models|default] [arguments]",
			Summary:     "Manage the LLM connections your agents use",
			Description: "A connection is either a provider credential — a key for openai, anthropic, gemini, fireworks or openrouter — or a custom-<name> endpoint with its own base URL and model list. Only an endpoint-backed connection contributes models you can pick a default from, because only those name their models. The word alone lists them. Stored credentials are never printed back, not even masked.",
			// As with ssh-key: the bare word lists and takes --json, while add,
			// rm and default write and therefore do not.
			JSON: true,
			Subcommands: []subSpec{
				{
					Name:  "list",
					Usage: "llm list [--json]",
					Desc:  "list your connections, without their credentials",
					JSON:  true,
					Run:   cmdLLMList,
				},
				{
					Name:  "add",
					Usage: "llm add <provider> [key] [--base-url=U] [--models=a,b] [--protocol=P]",
					Desc:  "store or replace one provider's settings",
					Args: []argSpec{
						{Name: "provider", Desc: "openai, anthropic, gemini, fireworks, openrouter or custom-<name>"},
						{Name: "key", Desc: "the credential; a custom endpoint may go without one", Optional: true},
					},
					Flags: []flagSpec{
						{Name: "base-url", Desc: "the complete API prefix, including /v1 where the endpoint wants one", Value: true},
						{Name: "models", Desc: "comma-separated model IDs the endpoint serves", Value: true},
						{Name: "protocol", Desc: "wire protocol: openai, openai-responses, anthropic or gemini", Value: true},
					},
					Run: cmdLLMAdd,
				},
				{
					Name:  "rm",
					Usage: "llm rm <provider>",
					Desc:  "delete that provider's connection",
					Args:  []argSpec{{Name: "provider", Desc: "the connection to delete"}},
					Run:   cmdLLMRemove,
				},
				{
					Name:  "models",
					Usage: "llm models [--json]",
					Desc:  "list the models you can put a VM on",
					JSON:  true,
					Run:   cmdLLMModels,
				},
				{
					Name:  "default",
					Usage: "llm default <model|auto>",
					Desc:  "choose the model your VMs open on",
					Args:  []argSpec{{Name: "model", Desc: "a model ID from \"llm models\", or \"auto\" to let the gateway pick"}},
					Run:   cmdLLMDefault,
				},
			},
			Examples: []string{
				"llm list",
				"llm add openai sk-...",
				"llm add openrouter sk-or-v1-... --models=openai/gpt-5",
				"llm add custom-lab sk-lab-... --base-url=https://lab.example/v1 --models=gpt-5,gpt-5-mini",
				"llm models --json",
				"llm default svkexe_user:custom-lab:gpt-5",
				"llm default auto",
			},
			Run: cmdLLMList,
		},
	}
}

// --- whoami ---

// accountJSON is what "whoami --json" emits.
type accountJSON struct {
	Email        string       `json:"email"`
	DisplayName  string       `json:"display_name,omitempty"`
	Role         string       `json:"role"`
	Admin        bool         `json:"admin"`
	CreatedAt    string       `json:"created_at"`
	AgeDays      int          `json:"age_days"`
	VMs          int          `json:"vms"`
	DefaultModel string       `json:"default_model"`
	SSHKeys      []sshKeyJSON `json:"ssh_keys"`
}

// sshKeyJSON describes one registered key. It carries the fingerprint and never
// the key body: the body is what an agent could paste elsewhere, and nothing
// here needs it to tell one key from another.
type sshKeyJSON struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   string `json:"created_at"`
}

func cmdWhoami(c *cmdCtx) error {
	// The default model is read back rather than taken from c.user: the session's
	// user was loaded by SSH fingerprint, and that lookup does not select it.
	chosen, err := c.s.db.UserDefaultModel(c.user.ID)
	if err != nil {
		return err
	}
	containers, err := c.s.db.ListContainersByOwner(c.user.ID)
	if err != nil {
		return err
	}
	keys, err := c.s.db.ListSSHKeysByUser(c.user.ID)
	if err != nil {
		return err
	}

	if c.json {
		view := accountJSON{
			Email:        c.user.Email,
			DisplayName:  c.user.DisplayName,
			Role:         c.user.Role,
			Admin:        isAdmin(c.user),
			CreatedAt:    formatTime(c.user.CreatedAt),
			AgeDays:      ageInDays(c.user.CreatedAt),
			VMs:          len(containers),
			DefaultModel: chosen,
			SSHKeys:      make([]sshKeyJSON, 0, len(keys)),
		}
		for _, k := range keys {
			view.SSHKeys = append(view.SSHKeys, newSSHKeyJSON(k))
		}
		return c.writeJSON(view)
	}

	c.print("\n")
	c.printf("  Email:      %s\n", c.user.Email)
	if c.user.DisplayName != "" {
		c.printf("  Name:       %s\n", c.user.DisplayName)
	}
	c.printf("  Role:       %s\n", c.user.Role)
	c.printf("  Created:    %s (%s)\n", c.user.CreatedAt.Format("2006-01-02 15:04"), accountAge(c.user.CreatedAt))
	c.printf("  VMs:        %d\n", len(containers))
	c.printf("  Model:      %s\n", modelSummary(chosen))
	if len(keys) == 0 {
		c.print("  SSH keys:   none registered\n")
	} else {
		c.print("\n  SSH keys:\n")
		for _, k := range keys {
			c.printf("    %-20s %s\n", keyName(k), k.Fingerprint)
		}
	}
	c.print("\n")
	return nil
}

// --- ssh-key ---

func cmdSSHKeyList(c *cmdCtx) error {
	keys, err := c.s.db.ListSSHKeysByUser(c.user.ID)
	if err != nil {
		return err
	}

	if c.json {
		views := make([]sshKeyJSON, 0, len(keys))
		for _, k := range keys {
			views = append(views, newSSHKeyJSON(k))
		}
		return c.writeJSON(views)
	}

	if len(keys) == 0 {
		c.print("No SSH keys registered. Add one with \"ssh-key add <name> <public-key>\".\n")
		return nil
	}
	c.print("\n")
	c.printf("  %-20s %-52s %s\n", "NAME", "FINGERPRINT", "ADDED")
	c.printf("  %-20s %-52s %s\n", "----", "-----------", "-----")
	for _, k := range keys {
		c.printf("  %-20s %-52s %s\n", keyName(k), k.Fingerprint, k.CreatedAt.Format("2006-01-02"))
	}
	c.print("\n")
	return nil
}

func cmdSSHKeyAdd(c *cmdCtx) error {
	name := strings.TrimSpace(c.arg(0))
	// The key argument is declared Repeated, so the parser has already folded
	// the rest of the line into it.
	body := strings.TrimSpace(c.arg(1))
	if name == "" || body == "" {
		return usagef("name the key and give the whole public key line")
	}

	pubKey, _, _, _, err := gossh.ParseAuthorizedKey([]byte(body))
	if err != nil {
		return fmt.Errorf("that is not an OpenSSH public key: paste the whole line from a .pub file, such as \"ssh-ed25519 AAAA... you@host\"")
	}
	fingerprint := gossh.FingerprintSHA256(pubKey)

	key := &db.SSHKey{
		ID:          uuid.New().String(),
		UserID:      c.user.ID,
		Fingerprint: fingerprint,
		PublicKey:   body,
		Name:        name,
	}
	if err := c.s.db.CreateSSHKey(key); err != nil {
		// The fingerprint is unique across the whole deployment, so a collision
		// is answered without saying whose account already holds it.
		if strings.Contains(err.Error(), "UNIQUE") {
			return fmt.Errorf("that key is already registered; \"ssh-key list\" shows the keys on this account")
		}
		return err
	}

	c.printf("SSH key %q added.\n", name)
	c.printf("  %s\n", fingerprint)
	return nil
}

func cmdSSHKeyRemove(c *cmdCtx) error {
	name := strings.TrimSpace(c.arg(0))
	if name == "" {
		return usagef("name the key to remove")
	}

	keys, err := c.s.db.ListSSHKeysByUser(c.user.ID)
	if err != nil {
		return err
	}
	var target *db.SSHKey
	for _, k := range keys {
		if k.Name == name {
			target = k
			break
		}
	}
	if target == nil {
		return fmt.Errorf("no SSH key named %q; \"ssh-key list\" shows the ones you have", name)
	}
	// The session already knows which key authenticated it, so refusing to
	// remove that one costs nothing and stops the common way of locking yourself
	// out: deleting the key you are holding, from the machine that holds it.
	if fp := c.sessionFingerprint(); fp != "" && fp == target.Fingerprint {
		return fmt.Errorf("SSH key %q is the key this session authenticated with; remove it from another session or from the dashboard", name)
	}
	if err := c.s.db.DeleteSSHKey(target.ID, c.user.ID); err != nil {
		return err
	}

	c.printf("SSH key %q removed.\n", name)
	return nil
}

// sessionFingerprint is the fingerprint of the key this session authenticated
// with, or the empty string when there is no session to ask — a one-shot
// invocation driven by a test, or a session that arrived without a key.
func (c *cmdCtx) sessionFingerprint() string {
	if c.sess == nil {
		return ""
	}
	key := c.sess.PublicKey()
	if key == nil {
		return ""
	}
	return gossh.FingerprintSHA256(key)
}

// --- llm ---

// llmConnectionJSON describes one stored connection. There is deliberately no
// field for the credential: it is not printed, not masked and not returned, so
// that no output of this shell can ever be a place a key leaks from.
type llmConnectionJSON struct {
	Provider  string   `json:"provider"`
	BaseURL   string   `json:"base_url,omitempty"`
	Protocol  string   `json:"protocol,omitempty"`
	Models    []string `json:"models,omitempty"`
	CreatedAt string   `json:"created_at"`
}

// llmModelsJSON is what "llm models --json" emits.
type llmModelsJSON struct {
	Models  []string `json:"models"`
	Default string   `json:"default"`
}

func cmdLLMList(c *cmdCtx) error {
	keys, err := c.s.db.ListAPIKeysByOwner(c.user.ID)
	if err != nil {
		return err
	}

	if c.json {
		views := make([]llmConnectionJSON, 0, len(keys))
		for _, k := range keys {
			views = append(views, llmConnectionJSON{
				Provider:  k.Provider,
				BaseURL:   k.BaseURL,
				Protocol:  k.Protocol,
				Models:    modelList(k.Models),
				CreatedAt: formatTime(k.CreatedAt),
			})
		}
		return c.writeJSON(views)
	}

	if len(keys) == 0 {
		c.print("No LLM connections. Add one with \"llm add <provider> <key>\".\n")
		return nil
	}
	c.print("\n")
	for _, k := range keys {
		c.printf("  %s\n", k.Provider)
		if k.BaseURL != "" {
			c.printf("    Base URL:  %s\n", k.BaseURL)
			c.printf("    Protocol:  %s\n", k.Protocol)
			c.printf("    Models:    %s\n", strings.Join(modelList(k.Models), ", "))
		} else {
			c.print("    Endpoint:  the provider's own\n")
		}
		c.printf("    Added:     %s\n", k.CreatedAt.Format("2006-01-02 15:04"))
	}
	c.print("\n")
	return nil
}

func cmdLLMAdd(c *cmdCtx) error {
	provider := strings.ToLower(strings.TrimSpace(c.arg(0)))
	if provider == "" {
		return usagef("name the provider to configure")
	}
	key := c.arg(1)

	// NormalizeProvider explains exactly which combination is wrong — that a
	// custom provider needs a base URL, that an endpoint needs model IDs — so its
	// own text is what the caller is shown.
	baseURL, models, protocol, err := db.NormalizeProvider(provider, c.flag("base-url"), c.flag("models"), key, c.flag("protocol"))
	if err != nil {
		return err
	}
	if err := c.s.db.SaveProviderKey(uuid.New().String(), c.user.ID, provider, key, baseURL, models, protocol, c.s.encKey); err != nil {
		return fmt.Errorf("store the connection: %w", err)
	}

	c.printf("LLM connection %q saved.\n", provider)
	if models != "" {
		c.printf("  Models: %s\n", strings.Join(modelList(models), ", "))
		c.print("  Choose the one your VMs open on with \"llm default <model>\"; \"llm models\" lists them.\n")
	}
	c.reportLLMSync("the connection")
	return nil
}

func cmdLLMRemove(c *cmdCtx) error {
	provider := strings.ToLower(strings.TrimSpace(c.arg(0)))
	if provider == "" {
		return usagef("name the provider to remove")
	}

	keys, err := c.s.db.ListAPIKeysByOwner(c.user.ID)
	if err != nil {
		return err
	}
	removed := false
	for _, k := range keys {
		if strings.ToLower(k.Provider) != provider {
			continue
		}
		if err := c.s.db.DeleteAPIKeyForOwner(k.ID, c.user.ID); err != nil {
			return fmt.Errorf("delete the connection: %w", err)
		}
		removed = true
	}
	if !removed {
		return fmt.Errorf("no LLM connection for %q; \"llm list\" shows the ones you have", provider)
	}

	c.printf("LLM connection %q removed.\n", provider)
	// Deleting a connection can retire the very model the VMs open on, which the
	// delete clears; the sync is what moves them off it.
	c.reportLLMSync("the removal")
	return nil
}

func cmdLLMModels(c *cmdCtx) error {
	models, err := c.s.db.OwnerModelIDs(c.user.ID)
	if err != nil {
		return err
	}
	chosen, err := c.s.db.UserDefaultModel(c.user.ID)
	if err != nil {
		return err
	}

	if c.json {
		if models == nil {
			models = []string{}
		}
		return c.writeJSON(llmModelsJSON{Models: models, Default: chosen})
	}

	if len(models) == 0 {
		c.print("No models to choose from: only a connection with a base URL and a model list\n")
		c.print("names its models, and you have none. Add one with\n")
		c.print("\"llm add custom-<name> <key> --base-url=<url> --models=<a,b>\".\n")
		return nil
	}
	c.print("\n")
	for _, model := range models {
		marker := " "
		if model == chosen {
			marker = "*"
		}
		c.printf("  %s %s\n", marker, model)
	}
	c.printf("\n  %s\n\n", modelSummary(chosen))
	return nil
}

func cmdLLMDefault(c *cmdCtx) error {
	choice := strings.TrimSpace(c.arg(0))
	if choice == "" {
		return usagef("name the model to open on, or \"auto\" to let the gateway pick")
	}
	// "auto" is the word for the empty string: a shell has no way to type one,
	// and leaving the argument off has to stay a usage error rather than silently
	// clearing a choice.
	if choice == "auto" {
		choice = ""
	}

	switch err := c.s.db.SetUserDefaultModel(c.user.ID, choice); {
	case errors.Is(err, db.ErrUnknownModel):
		return fmt.Errorf("%w; %s", err, reachableModels(c))
	case err != nil:
		return fmt.Errorf("save the default model: %w", err)
	}

	c.printf("%s\n", modelSummary(choice))
	// Repeated unchanged: re-running this after a failed sync has to retry it,
	// which it cannot do if an unmoved value skips the sync.
	c.reportLLMSync("the default model")
	return nil
}

// reachableModels renders what the caller could have chosen instead, so that a
// rejected model is answered with the list rather than with a second command to
// run.
func reachableModels(c *cmdCtx) string {
	models, err := c.s.db.OwnerModelIDs(c.user.ID)
	if err != nil || len(models) == 0 {
		return "no connection of yours names any model, so there is nothing to choose but \"auto\""
	}
	return "choose one of: " + strings.Join(models, ", ") + ", or \"auto\""
}

// syncLLM pushes the owner's current LLM settings into every running VM they
// own, as the dashboard and the REST API do.
func (c *cmdCtx) syncLLM() error {
	if c.s.materializer == nil || c.s.runtime == nil {
		return nil
	}
	containers, err := c.s.db.ListContainersByOwner(c.user.ID)
	if err != nil {
		return err
	}
	chosen, err := c.s.db.UserDefaultModel(c.user.ID)
	if err != nil {
		return err
	}
	var syncErr error
	for _, container := range containers {
		if container.Status == "running" {
			syncErr = errors.Join(syncErr, picoclaw.RefreshProviderKeys(c.ctx, c.s.runtime, c.s.materializer, container.ID, container.IncusName, c.user.ID, chosen, c.s.picoclawLLMCfg))
		}
	}
	return syncErr
}

// reportLLMSync runs the sync and reports a failure as a warning rather than as
// the command's error: the settings are stored either way, and a VM picks them
// up on its next start. Returning an error here would tell the caller their
// change did not happen, and invite them to make it again.
func (c *cmdCtx) reportLLMSync(what string) {
	if err := c.syncLLM(); err != nil {
		c.printf("Warning: %s was saved, but syncing it to your running VMs failed: %v\n", what, err)
		c.print("Restart the VM, or repeat the command, to retry.\n")
	}
}

// --- shared renderers ---

func newSSHKeyJSON(k *db.SSHKey) sshKeyJSON {
	return sshKeyJSON{
		Name:        keyName(k),
		Fingerprint: k.Fingerprint,
		CreatedAt:   formatTime(k.CreatedAt),
	}
}

// keyName names a key that was stored without one, which the dashboard allowed
// before a name was required.
func keyName(k *db.SSHKey) string {
	if k.Name == "" {
		return "(unnamed)"
	}
	return k.Name
}

// modelList splits the stored comma-separated model IDs, dropping the empty
// element a connection without models leaves behind.
func modelList(models string) []string {
	var out []string
	for _, model := range strings.Split(models, ",") {
		if model != "" {
			out = append(out, model)
		}
	}
	return out
}

// modelSummary states the model the owner's VMs open on, spelling out what the
// empty stored value means rather than printing nothing.
func modelSummary(chosen string) string {
	if chosen == "" {
		return "auto (the gateway picks the model)"
	}
	return chosen
}

func ageInDays(created time.Time) int {
	return int(time.Since(created).Hours() / 24)
}

func accountAge(created time.Time) string {
	switch days := ageInDays(created); days {
	case 0:
		return "today"
	case 1:
		return "1 day old"
	default:
		return fmt.Sprintf("%d days old", days)
	}
}
