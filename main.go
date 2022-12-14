package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/gonvenience/ytbx"
	"github.com/homeport/dyff/pkg/dyff"
	"github.com/mitchellh/go-homedir"
	"github.com/rs/zerolog"
	log "github.com/rs/zerolog/log"
	"github.com/stretchr/objx"

	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

var (
	repo           string
	kubeConfigFile string
	context        string
	namespace      string
	revision       int
	output         string
	settings       = cli.New()
)

type ChangeRequest struct {
	Path    string
	Content reflect.Value
}

type Changes struct {
	Items []*ChangeRequest
}

func init() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	// TODO: Derive namespace and context from kubeconfig
	defaultKubeConfigPath, err := findKubeConfig()
	if err != nil {
		log.Warn().AnErr("kubeConfigPath", err).Msg("Unable to determine default kubeconfig path")
	}

	flag.StringVar(&repo, "repo", "", "chart repository url where to locate the requested chart")
	flag.IntVar(&revision, "revision", 0, "specify a revision constraint for the chart revision to use. This constraint can be a specific tag (e.g. 1.1.1) or it may reference a valid range (e.g. ^2.0.0). If this is not specified, the latest revision is used")
	flag.StringVar(&kubeConfigFile, "kubeconfig", defaultKubeConfigPath, "path to the kubeconfig file")
	flag.StringVar(&context, "kube-context", "", "name of the kubeconfig context to use")
	flag.StringVar(&namespace, "namespace", "", "namespace scope for this request")
	flag.StringVar(&output, "output", "stdout", "output format. One of: (yaml,stdout)")
}

func main() {
	flag.Parse()
	if repo == "" {
		log.Error().Msg("missing -repo flag")
		flag.Usage()
		os.Exit(2)
	}

	if revision == 0 {
		log.Info().Msg("revision not specified. default: 0")
	}

	cwd, err := os.Getwd()
	if err != nil {
		log.Panic().Err(err)
	}

	helm, err := NewHelmClient()
	if err != nil {
		log.Panic().Err(err).Msg("fetching helm client")
	}

	rv, err := HelmFetch(helm)
	if err != nil {
		log.Panic().Err(err).Msg("fetching helm repo")
	}
	releaseValues, err := yaml.Marshal(rv)
	if err != nil {
		log.Error().Err(err).Msg("error while Marshaling")
	}

	err = CreateOutputFile(releaseValues, "examples/upstream-values-tmp.yaml")
	if err != nil {
		log.Panic().Err(err).Msg("unable to write data into the file")
	}

	downstreamFile := file(filepath.Join(cwd, "examples", "upstream-values-tmp.yaml"))
	upstreamFile := file(filepath.Join(cwd, "examples", "downstream-values-tmp.yaml"))
	diff, err := dyff.CompareInputFiles(downstreamFile, upstreamFile)
	if err != nil {
		log.Panic().Err(err).Msg("unable to compare input files")
	}

	changes := objx.Map{}
	for _, v := range diff.Diffs {
		changes = DetectChangedValues(v, changes)
	}

	yamlOutput, err := yaml.Marshal(&changes)
	if err != nil {
		log.Error().Err(err).Msg("error while Marshaling")
	}

	switch output {
	case "yaml":
		log.Info().Msgf("diff detected %v", changes)
		err := CreateOutputFile(yamlOutput, "examples/generated-values-tmp.yaml")
		if err != nil {
			log.Panic().Err(err).Msg("unable to write data into the file")
		}
	default:
		log.Info().Msgf("diffdetected %v", changes)
	}
}

func DetectChangedValues(diff dyff.Diff, changes objx.Map) objx.Map {
	var keyPath []string
	for _, e := range diff.Path.PathElements {
		keyPath = append(keyPath, e.Name)
	}
	keys := strings.Join(keyPath, ".")
	fmt.Println()
	changes.Set(keys, diff.Details[0].From.Value)
	return changes
}

func file(input string) ytbx.InputFile {
	inputfile, err := ytbx.LoadFile(input)
	if err != nil {
		log.Panic().Err(err).Msg("failed to load input file")
	}
	return inputfile
}

func findKubeConfig() (string, error) {
	env := os.Getenv("KUBECONFIG")
	if env != "" {
		return env, nil
	}
	path, err := homedir.Expand("~/.kube/config")
	if err != nil {
		return "", err
	}
	return path, nil
}

func CreateOutputFile(yamlOutput []byte, path string) error {
	log.Info().Msgf("creating file: %s", path)
	err := ioutil.WriteFile(path, yamlOutput, 0644)
	if err != nil {
		return err
	}
	return nil
}

func NewHelmClient() (*action.Configuration, error) {
	actionConfig := new(action.Configuration)

	settings.KubeContext = context
	settings.KubeConfig = kubeConfigFile
	if namespace == "" {
		namespace = settings.Namespace()
	}
	err := actionConfig.Init(settings.RESTClientGetter(), namespace, os.Getenv("HELM_DRIVER"), func(format string, v ...interface{}) {
		log.Info().Msgf(format, v)
	})
	if err != nil {
		return nil, err
	}
	return actionConfig, nil
}

func HelmFetch(h *action.Configuration) (map[string]interface{}, error) {
	c := action.NewGet(h)
	rel, err := c.Run(repo)
	if err != nil {
		return nil, err
	}

	// TODO: Fix release revision logic
	var previousRelease int
	if revision == 0 {
		previousRelease = rel.Version - 1
	} else {
		previousRelease = revision
	}

	val := action.NewGetValues(h)
	val.Version = previousRelease
	val.AllValues = true

	relVal, err := val.Run(rel.Name)
	if err != nil {
		return nil, err
	}

	return relVal, nil
}
