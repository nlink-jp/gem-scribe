package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nlink-jp/gem-scribe/internal/asr"
	"github.com/nlink-jp/gem-scribe/internal/config"
	"github.com/nlink-jp/gem-scribe/internal/enrich"
	"github.com/nlink-jp/gem-scribe/internal/staging"
	"github.com/nlink-jp/gem-scribe/internal/transcript"
	"github.com/spf13/cobra"
)

var (
	flagOutput    string
	flagFormat    string
	flagLanguages []string
	flagModel     string
	flagLocation  string
	flagDiarize   bool
	flagWordTimes bool
	flagSmart     bool
	flagQuiet     bool
	flagTranslate string
	flagSpeakers  []string
)

func init() {
	f := rootCmd.Flags()
	f.StringVarP(&flagOutput, "output-file", "o", "", "Write to this file instead of stdout")
	f.StringVarP(&flagFormat, "format", "f", string(transcript.FormatJSON),
		"Output format: "+strings.Join(formatNames(), ", "))
	f.StringSliceVar(&flagLanguages, "lang", nil,
		"BCP-47 language hints, e.g. --lang ja-JP (default: automatic detection)")
	f.StringVarP(&flagModel, "model", "m", "", "Transcription model to use")
	f.StringVar(&flagLocation, "location", "", "Vertex AI location (Gemini 3 models are served from global only)")
	f.BoolVar(&flagDiarize, "diarize", true, "Label each speaker turn")
	f.BoolVar(&flagWordTimes, "word-timestamps", true, "Ask for word-level timings, which give segments their start and end")
	f.BoolVar(&flagSmart, "smart", false, "Remove disfluencies and format lightly (cannot be combined with --diarize or --word-timestamps)")
	f.BoolVarP(&flagQuiet, "quiet", "q", false, "Suppress progress reporting on stderr")
	f.StringVar(&flagTranslate, "translate", "",
		"Add a translation beside the original, e.g. --translate en (a second pass over the transcript text)")
	f.StringSliceVar(&flagSpeakers, "speaker-hint", nil,
		"Candidate speaker names; assigns them to spk:N in a second pass (repeatable)")

	rootCmd.Args = cobra.MaximumNArgs(1)
	rootCmd.RunE = runTranscribe
}

func formatNames() []string {
	names := make([]string, 0, len(transcript.Formats()))
	for _, f := range transcript.Formats() {
		names = append(names, string(f))
	}
	return names
}

func runTranscribe(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		// No audio and no subcommand: show the help rather than a bare error,
		// because a lone `gem-scribe` is far more often a request for help
		// than a mistyped filename.
		return cmd.Help()
	}
	input := args[0]

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	cfg.ApplyFlags(flagModel, flagLocation,
		changedBool(cmd, "diarize", flagDiarize),
		changedBool(cmd, "word-timestamps", flagWordTimes))

	format, err := transcript.ParseFormat(flagFormat)
	if err != nil {
		return err
	}

	opts := asr.Options{
		Languages:      flagLanguages,
		Diarize:        cfg.Diarize(),
		WordTimestamps: cfg.WordTimestamps(),
		Smart:          flagSmart,
	}
	// SMART mode is incompatible with the two flags that are on by default, so
	// asking for it alone must not trip the conflict the user never chose.
	if opts.Smart {
		if !cmd.Flags().Changed("diarize") {
			opts.Diarize = false
		}
		if !cmd.Flags().Changed("word-timestamps") {
			opts.WordTimestamps = false
		}
	}
	if err := opts.Validate(); err != nil {
		return err
	}
	if err := requireTimingsFor(format, opts); err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	report := progressReporter(cmd)
	result, err := transcribeOne(ctx, cfg, input, opts, report)
	if err != nil {
		return err
	}

	if err := enrichResult(ctx, cfg, &result, flagTranslate, flagSpeakers, report); err != nil {
		return err
	}

	files, err := transcript.Render(result, format)
	if err != nil {
		return err
	}
	return writeFiles(cmd.OutOrStdout(), files, flagOutput)
}

// transcribeOne runs the whole pipeline for one input: stage it, transcribe it,
// and wrap the segments in the output envelope.
func transcribeOne(ctx context.Context, cfg *config.Config, input string, opts asr.Options, report func(string)) (transcript.Result, error) {
	var uploader staging.Uploader
	if cfg.Staging.Bucket != "" {
		up, err := staging.NewGCSUploader(ctx, cfg.Staging.Bucket, cfg.Staging.KeepStaged)
		if err != nil {
			return transcript.Result{}, err
		}
		defer func() { _ = up.Close() }()
		uploader = up
	}

	if !staging.IsGCSURI(input) {
		report(fmt.Sprintf("Reading %s...", filepath.Base(input)))
	}
	src, err := staging.Resolve(ctx, input, uploader)
	if err != nil {
		return transcript.Result{}, err
	}
	defer func() {
		if err := src.Cleanup(); err != nil {
			// Cleanup failing does not invalidate a transcript that already
			// succeeded, but a staged recording left in a bucket is something
			// the user needs to know about.
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}()
	if src.URI != "" && !staging.IsGCSURI(input) {
		report("Staged through Cloud Storage (larger than the inline limit).")
	}

	client, err := asr.New(ctx, cfg)
	if err != nil {
		return transcript.Result{}, err
	}

	report(fmt.Sprintf("Transcribing with %s...", cfg.Model.Name))
	start := time.Now()
	segments, err := client.Transcribe(ctx, asr.Source{
		Data: src.Data, URI: src.URI, MIMEType: src.MIMEType,
	}, opts)
	if err != nil {
		return transcript.Result{}, err
	}
	report(fmt.Sprintf("Done in %s.", time.Since(start).Round(time.Millisecond)))

	result := transcript.Result{
		Metadata: transcript.Metadata{
			Source:       input,
			Model:        cfg.Model.Name,
			SpeakerHints: []string{},
			Engine:       "vertex-ai",
			Diarized:     opts.Diarize,
			Location:     cfg.GCP.Location,
		},
		Segments: segments,
	}
	if d := result.Duration(); d > 0 {
		result.Metadata.DurationSeconds = &d
	}
	result.Normalize()
	if err := result.Validate(); err != nil {
		return transcript.Result{}, err
	}
	return result, nil
}

// enrichResult runs the second pass over the finished transcript.
//
// It runs after the transcript exists and is valid, and it works on text rather
// than audio. A failure here therefore costs an enrichment, never the
// transcript — which is why partial results are reported rather than raised.
func enrichResult(ctx context.Context, cfg *config.Config, result *transcript.Result,
	translateTo string, speakerHints []string, report func(string),
) error {
	if translateTo == "" && len(speakerHints) == 0 {
		return nil
	}

	client, err := enrich.New(ctx, cfg)
	if err != nil {
		return err
	}

	if len(speakerHints) > 0 {
		report(fmt.Sprintf("Naming speakers with %s...", cfg.SecondPass.Model))
		named, err := client.NameSpeakers(ctx, result, speakerHints)
		if err != nil {
			return fmt.Errorf("name speakers: %w", err)
		}
		if named < len(result.Speakers()) {
			fmt.Fprintf(os.Stderr, "warning: %d of %d speakers could not be named from the transcript; they keep their labels\n",
				len(result.Speakers())-named, len(result.Speakers()))
		}
	}

	if translateTo != "" {
		report(fmt.Sprintf("Translating into %s with %s...", translateTo, cfg.SecondPass.Model))
		tr, err := client.Translate(ctx, result, translateTo)
		if err != nil {
			return fmt.Errorf("translate: %w", err)
		}
		// A pass that quietly left part of the meeting in the original
		// language must not look like a success.
		if tr.Untranslated > 0 {
			fmt.Fprintf(os.Stderr, "warning: %d of %d segments were not translated and keep only the original\n",
				tr.Untranslated, tr.Translated+tr.Untranslated)
		}
	}

	return result.Validate()
}

// requireTimingsFor refuses a subtitle format when nothing will carry time.
// Rendering every cue at 00:00:00 produces a file that looks valid and is
// useless, which is worse than declining up front.
func requireTimingsFor(format transcript.Format, opts asr.Options) error {
	if format != transcript.FormatSRT && format != transcript.FormatVTT {
		return nil
	}
	if opts.WordTimestamps {
		return nil
	}
	return fmt.Errorf("%s output needs timings: drop --word-timestamps=false (or --smart, which excludes them)", format)
}

// changedBool reports a boolean flag only when the user actually passed it, so
// a flag's default never overwrites a value set in the config file.
func changedBool(cmd *cobra.Command, name string, value bool) *bool {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	return &value
}

func progressReporter(cmd *cobra.Command) func(string) {
	if flagQuiet {
		return func(string) {}
	}
	// Progress goes to stderr so that `gem-scribe a.wav > out.json` stays a
	// clean transcript.
	return func(msg string) { fmt.Fprintln(cmd.ErrOrStderr(), msg) }
}

// writeFiles sends rendered output to stdout or to disk. A format that
// produces several files (subtitles for a multi-language transcript) cannot go
// to stdout, because concatenating them would silently corrupt both.
func writeFiles(stdout io.Writer, files []transcript.File, outputPath string) error {
	if outputPath == "" {
		if len(files) > 1 {
			return errors.New("this format produces several files; use -o to name them")
		}
		_, err := io.WriteString(stdout, files[0].Content)
		return err
	}

	for _, f := range files {
		path := outputPath
		if f.Suffix != "" {
			ext := filepath.Ext(outputPath)
			path = strings.TrimSuffix(outputPath, ext) + f.Suffix + ext
		}
		if err := os.WriteFile(path, []byte(f.Content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Fprintln(os.Stderr, "wrote "+path)
	}
	return nil
}
