package cli

import (
	_ "embed"
	"io/ioutil"

	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/hatchet-dev/hatchet/internal/config/database"
	"github.com/hatchet-dev/hatchet/internal/config/loader"
	"github.com/hatchet-dev/hatchet/internal/config/server"
	"github.com/hatchet-dev/hatchet/internal/encryption"
	"sigs.k8s.io/yaml"

	"github.com/spf13/cobra"
)

var certDir string
var generatedConfigDir string
var skip []string
var overwrite bool

const (
	StageCerts string = "certs"
	StageKeys  string = "keys"
	StageSeed  string = "seed"
)

var quickstartCmd = &cobra.Command{
	Use:   "quickstart",
	Short: "Command used to setup a Hatchet instance",
	Run: func(cmd *cobra.Command, args []string) {
		err := runQuickstart()

		if err != nil {
			red := color.New(color.FgRed)
			red.Printf("Error running [%s]:%s\n", cmd.Use, err.Error())
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(quickstartCmd)

	quickstartCmd.PersistentFlags().StringVar(
		&certDir,
		"cert-dir",
		"./certs",
		"path to the directory where certificates should be stored",
	)

	quickstartCmd.PersistentFlags().StringVar(
		&generatedConfigDir,
		"generated-config-dir",
		"./generated",
		"path to the directory where the generated config should be written",
	)

	quickstartCmd.PersistentFlags().StringArrayVar(
		&skip,
		"skip",
		[]string{},
		"a list of steps to skip. possible values are \"certs\"",
	)

	quickstartCmd.PersistentFlags().BoolVar(
		&overwrite,
		"overwrite",
		true,
		"whether generated files should be overwritten, if they exist",
	)
}

func runQuickstart() error {
	generated, err := loadBaseConfigFiles()

	if err != nil {
		return fmt.Errorf("could not get base config files: %w", err)
	}

	if !shouldSkip(StageCerts) {
		err := setupCerts(generated)

		if err != nil {
			return fmt.Errorf("could not setup certs: %w", err)
		}
	}

	if !shouldSkip(StageKeys) {
		err := generateKeys(generated)

		if err != nil {
			return fmt.Errorf("could not generate keys: %w", err)
		}
	}

	err = writeGeneratedConfig(generated)

	if err != nil {
		return fmt.Errorf("could not write generated config files: %w", err)
	}

	if !shouldSkip(StageSeed) {
		// reload config at this point
		configLoader := loader.NewConfigLoader(configDirectory)
		err = runSeed(configLoader)

		if err != nil {
			return fmt.Errorf("could not run seed: %w", err)
		}
	}

	return nil
}

func shouldSkip(stage string) bool {
	for _, skipStage := range skip {
		if stage == skipStage {
			return true
		}
	}

	return false
}

//go:embed certs/cluster-cert.conf
var ClusterCertConf []byte

//go:embed certs/internal-admin-client-cert.conf
var InternalAdminClientCertConf []byte

//go:embed certs/worker-client-cert.conf
var WorkerClientCertConf []byte

//go:embed certs/generate-certs.sh
var GenerateCertsScript string

type generatedConfigFiles struct {
	sc *server.ServerConfigFile
	dc *database.ConfigFile
}

func setupCerts(generated *generatedConfigFiles) error {
	color.New(color.FgGreen).Printf("Generating certificates in cert directory %s\n", certDir)

	// verify that bash and openssl are installed on the system
	if !commandExists("openssl") {
		return fmt.Errorf("openssl must be installed and available in your $PATH")
	}

	if !commandExists("bash") {
		return fmt.Errorf("bash must be installed and available in your $PATH")
	}

	// write certificate config files to system
	fullPathCertDir, err := filepath.Abs(certDir)

	if err != nil {
		return err
	}

	err = os.MkdirAll(fullPathCertDir, os.ModePerm)

	if err != nil {
		return fmt.Errorf("could not create cert directory: %w", err)
	}

	err = os.WriteFile(filepath.Join(fullPathCertDir, "./cluster-cert.conf"), ClusterCertConf, 0666)

	if err != nil {
		return fmt.Errorf("could not create cluster-cert.conf file: %w", err)
	}

	err = os.WriteFile(filepath.Join(fullPathCertDir, "./internal-admin-client-cert.conf"), InternalAdminClientCertConf, 0666)

	if err != nil {
		return fmt.Errorf("could not create internal-admin-client-cert.conf file: %w", err)
	}

	err = os.WriteFile(filepath.Join(fullPathCertDir, "./worker-client-cert.conf"), WorkerClientCertConf, 0666)

	if err != nil {
		return fmt.Errorf("could not create worker-client-cert.conf file: %w", err)
	}

	// run openssl commands
	c := exec.Command("bash", "-s", "-", fullPathCertDir)

	c.Stdin = strings.NewReader(GenerateCertsScript)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	err = c.Run()

	if err != nil {
		return err
	}

	generated.sc.TLS.TLSRootCAFile = filepath.Join(fullPathCertDir, "ca.cert")
	generated.sc.TLS.TLSCertFile = filepath.Join(fullPathCertDir, "client-internal-admin.pem")
	generated.sc.TLS.TLSKeyFile = filepath.Join(fullPathCertDir, "client-internal-admin.key")

	return nil
}

func generateKeys(generated *generatedConfigFiles) error {
	color.New(color.FgGreen).Printf("Generating encryption keys for Hatchet server\n")

	cookieHashKey, err := encryption.GenerateRandomBytes(8)

	if err != nil {
		return fmt.Errorf("could not generate hash key for instance: %w", err)
	}

	cookieBlockKey, err := encryption.GenerateRandomBytes(8)

	if err != nil {
		return fmt.Errorf("could not generate block key for instance: %w", err)
	}

	if overwrite || (generated.sc.Auth.Cookie.Secrets == "") {
		generated.sc.Auth.Cookie.Secrets = fmt.Sprintf("%s %s", cookieHashKey, cookieBlockKey)
	}

	return nil
}

func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

func commandExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

func loadBaseConfigFiles() (*generatedConfigFiles, error) {
	res := &generatedConfigFiles{}
	var err error

	res.dc, err = loader.LoadDatabaseConfigFile(getFiles("database.yaml")...)

	if err != nil {
		return nil, err
	}

	res.sc, err = loader.LoadServerConfigFile(getFiles("server.yaml")...)

	if err != nil {
		return nil, err
	}

	return res, nil
}

func shouldWriteConfig(conf string) bool {
	return overwrite || conf == ""
}

func getFiles(name string) [][]byte {
	files := [][]byte{}

	basePath := filepath.Join(configDirectory, name)

	if fileExists(basePath) {
		configFileBytes, err := ioutil.ReadFile(basePath)

		if err != nil {
			panic(err)
		}

		files = append(files, configFileBytes)
	}

	generatedPath := filepath.Join(generatedConfigDir, name)

	if fileExists(generatedPath) {
		generatedFileBytes, err := ioutil.ReadFile(filepath.Join(generatedConfigDir, name))

		if err != nil {
			panic(err)
		}

		files = append(files, generatedFileBytes)
	}

	return files
}

func writeGeneratedConfig(generated *generatedConfigFiles) error {
	color.New(color.FgGreen).Printf("Generating config files %s\n", generatedConfigDir)

	err := os.MkdirAll(generatedConfigDir, os.ModePerm)

	if err != nil {
		return fmt.Errorf("could not create generated config directory: %w", err)
	}

	databasePath := filepath.Join(generatedConfigDir, "./database.yaml")

	databaseConfigBytes, err := yaml.Marshal(generated.dc)

	if err != nil {
		return err
	}

	err = ioutil.WriteFile(databasePath, databaseConfigBytes, 0666)

	if err != nil {
		return fmt.Errorf("could not write database.yaml file: %w", err)
	}

	serverPath := filepath.Join(generatedConfigDir, "./server.yaml")

	serverConfigBytes, err := yaml.Marshal(generated.sc)

	if err != nil {
		return err
	}

	err = ioutil.WriteFile(serverPath, serverConfigBytes, 0666)

	if err != nil {
		return fmt.Errorf("could not write server.yaml file: %w", err)
	}

	return nil
}
