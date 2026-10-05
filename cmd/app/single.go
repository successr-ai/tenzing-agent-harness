package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	cfgfile "github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

const (
	modelTypeLLM       = "llm"
	modelTypeSystemOne = "systemone"
)

// singleOpts are `tenzing single`'s flags.
type singleOpts struct {
	configPath string
	provider   string
	model      string
	modelType  string
	// llm only.
	systemPrompt string
	thinking     *bool // nil keeps the provider default
	stream       bool
	stderr       io.Writer // streamed thinking goes here, keeping stdout the answer
}

// newSingleCmd builds `tenzing single`: one request straight to a model and
// its answer on stdout. No agent loop, tools, system prompt, context files or
// session — a passthrough for one-off calls.
func newSingleCmd() *cobra.Command {
	o := singleOpts{}
	var thinking bool
	cmd := &cobra.Command{
		Use:   "single [input]",
		Short: "Send one request straight to a model and print its answer",
		Long: "Sends one request to a model and prints the reply. No agent loop, tools,\n" +
			"system prompt or session. Input is the argument, or stdin when it is\n" +
			"omitted or \"-\".\n\n" +
			"--model-type llm (default): input is the prompt; prints the answer text\n" +
			"(--stream prints it as it arrives, with any reasoning on stderr).\n" +
			"--model-type systemone: input is {\"state\":…,\"questions\":{…}}; prints the\n" +
			"answers as JSON.\n\n" +
			"--provider names a providers: entry and makes --model the wire model id,\n" +
			"so no models: entry is needed. Without it, --model is an alias (default:\n" +
			"model: or systemone_model: from tenzing.yaml).",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := readSingleInput(args, cmd.InOrStdin())
			if err != nil {
				return err
			}
			cfgPath, explicit := resolveConfigPath(o.configPath, cmd.Flags().Changed("config"))
			file, found, err := cfgfile.Load(cfgPath, explicit)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("no config file found at %s: run 'tenzing init' to write a starter tenzing.yaml", cfgPath)
			}
			d, err := buildDeps(file.Providers, file.Models, nil)
			if err != nil {
				return fmt.Errorf("config %s: %w", cfgPath, err)
			}
			if cmd.Flags().Changed("thinking") {
				o.thinking = &thinking
			}
			o.stderr = cmd.ErrOrStderr()
			return runSingle(cmd.Context(), d, file, o, input, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&o.configPath, "config", "", "YAML config file (default ./tenzing.yaml, then <user config dir>/tenzing/tenzing.yaml; env TENZING_CONFIG)")
	fl.StringVar(&o.provider, "provider", "", "declared provider name; makes --model the wire model id")
	fl.StringVar(&o.model, "model", "", "model alias, or the wire model id with --provider (default: model: / systemone_model:)")
	fl.StringVar(&o.modelType, "model-type", modelTypeLLM, "llm (chat completion) or systemone (typed evaluation)")
	fl.StringVar(&o.systemPrompt, "system-prompt", "", "system prompt for the call (llm only; default none)")
	fl.BoolVar(&thinking, "thinking", false, "model reasoning on or off (llm only; default: provider default)")
	fl.BoolVar(&o.stream, "stream", false, "print the answer as it streams, reasoning to stderr (llm only)")
	return cmd
}

// readSingleInput takes the positional argument, or all of stdin when it is
// absent or "-".
func readSingleInput(args []string, stdin io.Reader) (string, error) {
	if len(args) == 1 && args[0] != "-" {
		return args[0], nil
	}
	b, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return string(b), nil
}

// runSingle resolves the model and makes the one call.
func runSingle(ctx context.Context, d *deps, file cfgfile.File, o singleOpts, input string, out io.Writer) error {
	if strings.TrimSpace(input) == "" {
		return errors.New("single: empty input")
	}
	if o.provider != "" && o.model == "" {
		return errors.New("--provider requires --model")
	}
	ref := o.model
	if o.provider != "" {
		b, err := json.Marshal(map[string]string{"provider": o.provider, "model_name": o.model})
		if err != nil {
			return err
		}
		ref = string(b)
	}

	switch o.modelType {
	case modelTypeLLM:
		if ref == "" {
			ref = file.Model
		}
		if ref == "" {
			return errors.New("no model selected: pass --model or set model: in tenzing.yaml")
		}
		return singleLLM(ctx, d, ref, o, input, out)
	case modelTypeSystemOne:
		if o.systemPrompt != "" || o.thinking != nil || o.stream {
			return errors.New("--system-prompt, --thinking and --stream apply to --model-type llm only")
		}
		if ref == "" {
			ref = file.SystemOneModel
		}
		if ref == "" {
			return errors.New("no System One model selected: pass --model or set systemone_model: in tenzing.yaml")
		}
		return singleSystemOne(ctx, d, ref, input, out)
	default:
		return fmt.Errorf("--model-type must be %s or %s, got %q", modelTypeLLM, modelTypeSystemOne, o.modelType)
	}
}

func singleLLM(ctx context.Context, d *deps, ref string, o singleOpts, prompt string, out io.Writer) error {
	rm, err := d.resolve(ref)
	if err != nil {
		return err
	}
	llm, err := d.llms.Get(rm)
	if err != nil {
		return err
	}
	req := common.CompletionRequest{
		Model:    llm.GetCurrentModel(),
		Messages: []common.Message{common.NewUserMessage(prompt)},
		System:   o.systemPrompt,
		Think:    o.thinking,
	}
	if o.stream {
		err = streamLLM(ctx, llm, req, out, o.stderr)
	} else {
		var resp common.CompletionResponse
		if resp, err = llm.SendSyncMessage(ctx, req); err == nil {
			_, err = fmt.Fprintln(out, resp.Text())
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %w", rm.Def.Name, err)
	}
	return nil
}

// streamLLM writes text deltas to out and reasoning deltas to errOut as they
// arrive. The channel is drained to the end even after an error event: the
// provider closes it on return, and leaving it unread would block it.
func streamLLM(ctx context.Context, llm common.LLM, req common.CompletionRequest, out, errOut io.Writer) error {
	events := make(chan common.StreamEvent)
	done := make(chan error, 1)
	go func() { done <- llm.SendStreamingMessage(ctx, req, events) }()

	var streamErr error
	thinking := false // reasoning printed but not yet ended with a newline
	for ev := range events {
		switch ev.Type {
		case common.StreamEventDelta:
			if thinking {
				fmt.Fprintln(errOut)
				thinking = false
			}
			fmt.Fprint(out, ev.Text)
		case common.StreamEventThinking:
			if errOut != nil {
				fmt.Fprint(errOut, ev.Text)
				thinking = true
			}
		case common.StreamEventError:
			streamErr = ev.Err
		}
	}
	if err := <-done; err != nil {
		return err
	}
	if streamErr != nil {
		return streamErr
	}
	_, err := fmt.Fprintln(out)
	return err
}

// singleSystemOne decodes the wire-shaped request, evaluates it and prints
// the wire-shaped response.
func singleSystemOne(ctx context.Context, d *deps, ref, input string, out io.Writer) error {
	var req struct {
		State     common.State               `json:"state"`
		Questions map[string]common.Question `json:"questions"`
	}
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return fmt.Errorf(`systemone input must be {"state":…,"questions":{…}}: %w`, err)
	}
	if req.State == nil || len(req.Questions) == 0 {
		return errors.New(`systemone input needs "state" and at least one entry in "questions"`)
	}
	rs, err := d.resolveSystemOne(ref)
	if err != nil {
		return err
	}
	judge, err := d.judges.GetSystemOne(rs)
	if err != nil {
		return err
	}
	resp, err := judge.Evaluate(ctx, common.EvaluationRequest{State: req.State, Questions: req.Questions})
	if err != nil {
		return fmt.Errorf("%s: %w", rs.Name, err)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"model":   resp.Model,
		"answers": resp.Answers,
		"usage":   map[string]int64{"input_tokens": resp.Usage.InputTokens, "output_tokens": resp.Usage.OutputTokens},
	})
}
