// thn – CLI for tahini
//
// Configuration (in order of precedence):
//   1. Flags:        --url, --token
//   2. Env vars:     THN_URL, THN_TOKEN
//   3. Config file:  ~/.config/thn/config (JSON: {"url":"...","token":"..."})
//
// Usage:
//   thn env list
//   thn env get   <id-or-name>
//   thn env create --blueprint <name> --name <name> [--params KEY=VAL ...]
//   thn env start  <id-or-name>
//   thn env stop   <id-or-name>
//   thn env delete <id-or-name>
//   thn blueprint list
//   thn blueprint get <id>
//   thn run get  <id>
//   thn run logs <id>
//   thn token list
//   thn token create --name <name>
//   thn token delete <id>
//   thn config set-url  <url>
//   thn config set-token <token>

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
)

// --- Config ---

type Config struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "thn", "config")
}

func loadConfig() Config {
	cfg := Config{}
	if data, err := os.ReadFile(configPath()); err == nil {
		json.Unmarshal(data, &cfg)
	}
	if v := os.Getenv("THN_URL"); v != "" {
		cfg.URL = v
	}
	if v := os.Getenv("THN_TOKEN"); v != "" {
		cfg.Token = v
	}
	return cfg
}

func saveConfig(cfg Config) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0600)
}

// --- HTTP client ---

type client struct {
	base  string
	token string
	http  *http.Client
}

func newClient(cfg Config) *client {
	return &client{
		base:  strings.TrimRight(cfg.URL, "/"),
		token: cfg.Token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *client) do(method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.base+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.http.Do(req)
}

func (c *client) get(path string, out any) error {
	resp, err := c.do("GET", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *client) post(path string, body, out any) error {
	resp, err := c.do("POST", path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *client) put(path string, body, out any) error {
	resp, err := c.do("PUT", path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *client) delete(path string) error {
	resp, err := c.do("DELETE", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readError(resp)
	}
	return nil
}

func readError(resp *http.Response) error {
	var e struct {
		Error string `json:"error"`
	}
	data, _ := io.ReadAll(resp.Body)
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		return fmt.Errorf("server error: %s", e.Error)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
}

// --- Response types ---

type Environment struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	BlueprintID   string    `json:"blueprint_id"`
	BlueprintName string    `json:"blueprint_name"`
	Status        string    `json:"status"`
	Params        string    `json:"params"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Blueprint struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	GitURL      string    `json:"git_url"`
	CreatedAt   time.Time `json:"created_at"`
}

type Run struct {
	ID            string     `json:"id"`
	EnvironmentID string     `json:"environment_id"`
	Transition    string     `json:"transition"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	FinishedAt    *time.Time `json:"finished_at"`
}

type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// --- Table writer ---

func tw() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

func fmtTime(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04")
}

func fmtTimePtr(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04")
}

// --- Commands ---

func cmdEnv(args []string, cli *client) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: thn env <list|get|create|start|stop|delete> ...")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "list", "ls":
		var envs []Environment
		if err := cli.get("/api/v1/environments", &envs); err != nil {
			return err
		}
		w := tw()
		fmt.Fprintln(w, "ID\tNAME\tBLUEPRINT\tSTATUS\tUPDATED")
		for _, e := range envs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.ID[:8], e.Name, e.BlueprintName, e.Status, fmtTime(e.UpdatedAt))
		}
		return w.Flush()

	case "get":
		if len(args) == 0 {
			return fmt.Errorf("usage: thn env get <id>")
		}
		var env Environment
		if err := cli.get("/api/v1/environments/"+args[0], &env); err != nil {
			return err
		}
		fmt.Printf("id:         %s\n", env.ID)
		fmt.Printf("name:       %s\n", env.Name)
		fmt.Printf("blueprint:  %s (%s)\n", env.BlueprintName, env.BlueprintID[:8])
		fmt.Printf("status:     %s\n", env.Status)
		fmt.Printf("updated:    %s\n", fmtTime(env.UpdatedAt))
		if env.Params != "" {
			fmt.Printf("params:\n")
			for _, line := range strings.Split(env.Params, "\n") {
				if line != "" {
					fmt.Printf("  %s\n", line)
				}
			}
		}
		return nil

	case "create":
		fs := flag.NewFlagSet("env create", flag.ContinueOnError)
		name := fs.String("name", "", "environment name (required)")
		bpID := fs.String("blueprint", "", "blueprint id or name (required)")
		var params strSlice
		fs.Var(&params, "param", "variable KEY=VALUE (repeatable)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *name == "" || *bpID == "" {
			return fmt.Errorf("--name and --blueprint are required")
		}
		var env Environment
		if err := cli.post("/api/v1/environments", map[string]any{
			"name":         *name,
			"blueprint_id": *bpID,
			"params":       strings.Join(params, "\n"),
		}, &env); err != nil {
			return err
		}
		fmt.Printf("created environment %s (%s)\n", env.Name, env.ID[:8])
		return nil

	case "start":
		if len(args) == 0 {
			return fmt.Errorf("usage: thn env start <id>")
		}
		var out map[string]string
		if err := cli.post("/api/v1/environments/"+args[0]+"/start", nil, &out); err != nil {
			return err
		}
		fmt.Printf("started — run id: %s\n", out["run_id"])
		return nil

	case "stop":
		if len(args) == 0 {
			return fmt.Errorf("usage: thn env stop <id>")
		}
		var out map[string]string
		if err := cli.post("/api/v1/environments/"+args[0]+"/stop", nil, &out); err != nil {
			return err
		}
		fmt.Printf("stopping — run id: %s\n", out["run_id"])
		return nil

	case "delete", "rm":
		if len(args) == 0 {
			return fmt.Errorf("usage: thn env delete <id>")
		}
		var out map[string]string
		if err := cli.post("/api/v1/environments/"+args[0]+"/delete", nil, &out); err != nil {
			return err
		}
		fmt.Printf("deleting — run id: %s\n", out["run_id"])
		return nil

	default:
		return fmt.Errorf("unknown env subcommand: %s", sub)
	}
}

func cmdBlueprint(args []string, cli *client) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: thn blueprint <list|get> ...")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "list", "ls":
		var bps []Blueprint
		if err := cli.get("/api/v1/blueprints", &bps); err != nil {
			return err
		}
		w := tw()
		fmt.Fprintln(w, "ID\tNAME\tDESCRIPTION\tCREATED")
		for _, b := range bps {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", b.ID[:8], b.Name, b.Description, fmtTime(b.CreatedAt))
		}
		return w.Flush()

	case "get":
		if len(args) == 0 {
			return fmt.Errorf("usage: thn blueprint get <id>")
		}
		var bp Blueprint
		if err := cli.get("/api/v1/blueprints/"+args[0], &bp); err != nil {
			return err
		}
		fmt.Printf("id:          %s\n", bp.ID)
		fmt.Printf("name:        %s\n", bp.Name)
		fmt.Printf("description: %s\n", bp.Description)
		fmt.Printf("git_url:     %s\n", bp.GitURL)
		fmt.Printf("created:     %s\n", fmtTime(bp.CreatedAt))
		return nil

	default:
		return fmt.Errorf("unknown blueprint subcommand: %s", sub)
	}
}

func cmdRun(args []string, cli *client) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: thn run <get|logs> <id>")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "get":
		if len(args) == 0 {
			return fmt.Errorf("usage: thn run get <id>")
		}
		var run Run
		if err := cli.get("/api/v1/runs/"+args[0], &run); err != nil {
			return err
		}
		fmt.Printf("id:          %s\n", run.ID)
		fmt.Printf("environment: %s\n", run.EnvironmentID)
		fmt.Printf("transition:  %s\n", run.Transition)
		fmt.Printf("status:      %s\n", run.Status)
		fmt.Printf("started:     %s\n", fmtTime(run.CreatedAt))
		fmt.Printf("finished:    %s\n", fmtTimePtr(run.FinishedAt))
		return nil

	case "logs":
		if len(args) == 0 {
			return fmt.Errorf("usage: thn run logs <id>")
		}
		var out map[string]string
		if err := cli.get("/api/v1/runs/"+args[0]+"/logs", &out); err != nil {
			return err
		}
		fmt.Print(out["logs"])
		return nil

	default:
		return fmt.Errorf("unknown run subcommand: %s", sub)
	}
}

func cmdToken(args []string, cli *client) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: thn token <list|create|delete> ...")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "list", "ls":
		var tokens []Token
		if err := cli.get("/api/v1/tokens", &tokens); err != nil {
			return err
		}
		w := tw()
		fmt.Fprintln(w, "ID\tNAME\tCREATED\tLAST USED")
		for _, t := range tokens {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", t.ID[:8], t.Name, fmtTime(t.CreatedAt), fmtTimePtr(t.LastUsedAt))
		}
		return w.Flush()

	case "create":
		fs := flag.NewFlagSet("token create", flag.ContinueOnError)
		name := fs.String("name", "", "token name (required)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *name == "" && len(fs.Args()) > 0 {
			*name = fs.Args()[0]
		}
		if *name == "" {
			return fmt.Errorf("--name is required")
		}
		var out map[string]any
		if err := cli.post("/api/v1/tokens", map[string]string{"name": *name}, &out); err != nil {
			return err
		}
		fmt.Printf("token created — copy it now, it won't be shown again:\n\n  %s\n\n", out["token"])
		fmt.Printf("id: %s\n", out["id"])
		return nil

	case "delete", "rm":
		if len(args) == 0 {
			return fmt.Errorf("usage: thn token delete <id>")
		}
		if err := cli.delete("/api/v1/tokens/" + args[0]); err != nil {
			return err
		}
		fmt.Println("token deleted")
		return nil

	default:
		return fmt.Errorf("unknown token subcommand: %s", sub)
	}
}

func cmdConfig(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: thn config <set-url|set-token> <value>")
	}
	sub, args := args[0], args[1:]
	cfg := loadConfig()
	switch sub {
	case "set-url":
		if len(args) == 0 {
			return fmt.Errorf("usage: thn config set-url <url>")
		}
		cfg.URL = strings.TrimRight(args[0], "/")
		if err := saveConfig(cfg); err != nil {
			return err
		}
		fmt.Printf("url set to %s\n", cfg.URL)
		return nil

	case "set-token":
		if len(args) == 0 {
			return fmt.Errorf("usage: thn config set-token <token>")
		}
		cfg.Token = args[0]
		if err := saveConfig(cfg); err != nil {
			return err
		}
		fmt.Println("token saved to", configPath())
		return nil

	case "show":
		fmt.Printf("url:   %s\n", cfg.URL)
		if cfg.Token != "" {
			fmt.Printf("token: %s…\n", cfg.Token[:min(12, len(cfg.Token))])
		} else {
			fmt.Println("token: (not set)")
		}
		return nil

	default:
		return fmt.Errorf("unknown config subcommand: %s", sub)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `thn — tahini CLI

usage:
  thn [--url URL] [--token TOKEN] <command> [args]

commands:
  env list                               list environments
  env get   <id>                         get environment details
  env create --name <n> --blueprint <b>  create environment
  env start  <id>                        start environment
  env stop   <id>                        stop environment
  env delete <id>                        delete environment

  blueprint list                         list blueprints
  blueprint get <id>                     get blueprint details

  run get  <id>                          get run status
  run logs <id>                          print run logs

  token list                             list api tokens
  token create --name <name>             create api token (shown once)
  token delete <id>                      delete api token

  config set-url   <url>                 save server url to config
  config set-token <token>               save api token to config
  config show                            show current config

configuration:
  flags > env vars (THN_URL, THN_TOKEN) > %s

`, configPath())
}

func main() {
	fs := flag.NewFlagSet("thn", flag.ContinueOnError)
	fs.Usage = usage
	urlFlag := fs.String("url", "", "tahini server URL (overrides config/env)")
	tokenFlag := fs.String("token", "", "API token (overrides config/env)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	args := fs.Args()

	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	cmd, rest := args[0], args[1:]

	// config command doesn't need a server connection
	if cmd == "config" {
		if err := cmdConfig(rest); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	cfg := loadConfig()
	if *urlFlag != "" {
		cfg.URL = *urlFlag
	}
	if *tokenFlag != "" {
		cfg.Token = *tokenFlag
	}
	if cfg.URL == "" {
		fmt.Fprintln(os.Stderr, "error: no server URL configured\n  run: thn config set-url http://localhost:8080")
		os.Exit(1)
	}

	cli := newClient(cfg)

	var err error
	switch cmd {
	case "env":
		err = cmdEnv(rest, cli)
	case "blueprint", "bp":
		err = cmdBlueprint(rest, cli)
	case "run":
		err = cmdRun(rest, cli)
	case "token":
		err = cmdToken(rest, cli)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// strSlice is a flag.Value for repeatable --param flags.
type strSlice []string

func (s *strSlice) String() string  { return strings.Join(*s, ",") }
func (s *strSlice) Set(v string) error { *s = append(*s, v); return nil }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
