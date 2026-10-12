package main

import (
	"ec-csi-plugin/pkg/driver"
	"ec-csi-plugin/pkg/version"
	"fmt"
	edgecloudV2 "github.com/Edge-Center/edgecentercloud-go/v2"
	"github.com/mitchellh/go-homedir"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"k8s.io/component-base/cli"
	"log"
	"os"
)

var (
	endpoint          string
	pathToConfig      string
	cluster           string
	config            edgecloudV2.CloudConfig
	configuredRegions []regionConfig
)

type regionConfig struct {
	RegionID int    `mapstructure:"regionID"`
	Topology string `mapstructure:"topology"`
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&pathToConfig, "config", "", "config file")
	if err := rootCmd.MarkPersistentFlagRequired("config"); err != nil {
		log.Fatalf("unable to mark flag config to be required: %v", err)
	}

	rootCmd.PersistentFlags().StringVar(&endpoint, "endpoint", "", "CSI endpoint")
	if err := rootCmd.MarkPersistentFlagRequired("endpoint"); err != nil {
		log.Fatalf("unable to mark flag endpoint to be required: %v", err)
	}

	rootCmd.PersistentFlags().StringVar(&cluster, "cluster", "", "The identifier of the cluster that the plugin is running in.")
	if err := rootCmd.MarkPersistentFlagRequired("cluster"); err != nil {
		log.Fatalf("unable to mark flag cluster to be required: %v", err)
	}
}

var rootCmd = &cobra.Command{
	Use:   "Edgecenter",
	Short: "CSI based Edgecenter driver",
	Run: func(cmd *cobra.Command, args []string) {
		role := os.Getenv("CSI_ROLE")
		if role == "" {
			role = "controller"
		}
		if role != "controller" && role != "node" {
			log.Fatalf("CSI_ROLE must be controller or node, got %q", role)
		}
		if role == "controller" && config.APIToken == "" {
			log.Fatalf("apiToken or CSI_API_TOKEN must be provided for the controller")
		}

		regions := configuredRegions
		if len(regions) == 0 {
			regions = []regionConfig{{RegionID: config.RegionID, Topology: os.Getenv("CSI_TOPOLOGY_REGION")}}
		}
		if err := validateRegions(regions, config.RegionID, role, os.Getenv("CSI_TOPOLOGY_REGION")); err != nil {
			log.Fatalf("invalid region configuration: %v", err)
		}
		clouds := make(map[int]*edgecloudV2.Client, len(regions))
		topologies := make(map[int]string, len(regions))
		for _, region := range regions {
			if role == "controller" {
				cloud, err := edgecloudV2.NewWithRetries(nil,
					edgecloudV2.SetAPIKey(config.APIToken),
					edgecloudV2.SetBaseURL(config.APIUrl),
					edgecloudV2.SetProject(config.ProjectID),
					edgecloudV2.SetRegion(region.RegionID),
				)
				if err != nil {
					log.Fatalf("failed to create cloud client for region %d: %v", region.RegionID, err)
				}
				clouds[region.RegionID] = cloud
			}
			topologies[region.RegionID] = region.Topology
		}
		if len(topologies) == 0 {
			log.Fatalf("at least one EdgeCenter region must be configured")
		}
		if role == "controller" && len(clouds) == 0 {
			log.Fatalf("at least one EdgeCenter API client must be configured")
		}
		log.Printf("create new driver")
		d := driver.New(clouds, topologies, cluster, int64(config.RegionID), true)
		d.Start(endpoint)
		d.Wait()
	},
	Version: version.Version,
}

func validateRegions(regions []regionConfig, legacyRegionID int, role, nodeTopology string) error {
	if len(regions) == 0 {
		return fmt.Errorf("at least one EdgeCenter region must be configured")
	}

	ids := make(map[int]struct{}, len(regions))
	topologies := make(map[string]struct{}, len(regions))
	for _, region := range regions {
		if region.RegionID <= 0 {
			return fmt.Errorf("regionID must be positive for topology %q", region.Topology)
		}
		if _, exists := ids[region.RegionID]; exists {
			return fmt.Errorf("duplicate regionID %d", region.RegionID)
		}
		ids[region.RegionID] = struct{}{}
		if region.Topology == "" {
			if len(regions) > 1 {
				return fmt.Errorf("topology must be provided for regionID %d", region.RegionID)
			}
			continue
		}
		if _, exists := topologies[region.Topology]; exists {
			return fmt.Errorf("duplicate topology %q", region.Topology)
		}
		topologies[region.Topology] = struct{}{}
	}

	if legacyRegionID != 0 {
		if _, exists := ids[legacyRegionID]; !exists {
			return fmt.Errorf("legacy regionID %d is not present in regions", legacyRegionID)
		}
	} else if len(regions) > 1 {
		return fmt.Errorf("regionID must be set when multiple regions are configured")
	}
	if role == "node" {
		if nodeTopology == "" && len(regions) > 1 {
			return fmt.Errorf("CSI_TOPOLOGY_REGION must be provided for a multi-region node plugin")
		}
		if nodeTopology != "" {
			if _, exists := topologies[nodeTopology]; !exists {
				return fmt.Errorf("CSI_TOPOLOGY_REGION %q is not present in the region configuration", nodeTopology)
			}
		}
	}
	return nil
}

func main() {
	code := cli.Run(rootCmd)
	os.Exit(code)
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if pathToConfig != "" {
		// Use config file from the flag.
		viper.SetConfigFile(pathToConfig)
	} else {
		// Find home directory.
		home, err := homedir.Dir()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		viper.AddConfigPath(home)
		viper.SetConfigName("ec-csi-plugin.yaml")
	}

	viper.AutomaticEnv() // read in environment variables that match

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("failed to read config file: %s", err)
	}
	if err := viper.Unmarshal(&config); err != nil {
		log.Fatalf("unable to decode the configuration: %s", err)
	}
	if err := viper.UnmarshalKey("regions", &configuredRegions); err != nil {
		log.Fatalf("unable to decode regions: %s", err)
	}
	if config.APIToken == "" {
		config.APIToken = os.Getenv("CSI_API_TOKEN")
	}
}
