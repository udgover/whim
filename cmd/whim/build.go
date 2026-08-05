package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/udgover/whim/microvm"
)

// buildJSON is the stable, redacted result emitted by `whim build --json`.
type buildJSON struct {
	Name                string               `json:"name"`
	ARN                 string               `json:"arn"`
	Source              string               `json:"source"`
	Cached              bool                 `json:"cached"`
	Egress              string               `json:"egress"`
	EgressConnector     string               `json:"egress_connector,omitempty"`
	EgressResourceGroup *EgressResourceGroup `json:"egress_resource_group,omitempty"`
}

// renderBuildJSON writes one redacted build result object. Account IDs (when
// WHIM_REDACT_ACCOUNT=1) and source credentials are masked consistently.
// resourceGroup is nil unless --egress-auto-provision produced one; its
// resource IDs (vpc-*, subnet-*, ...) don't embed an account ID, so they are
// not redacted.
func renderBuildJSON(w io.Writer, name, arn, source, egress, egressConnector string, cached bool, resourceGroup *EgressResourceGroup) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(buildJSON{
		Name:                name,
		ARN:                 redactAccountID(arn),
		Source:              redactAccountID(redactSourceCreds(source)),
		Cached:              cached,
		Egress:              egress,
		EgressConnector:     redactAccountID(egressConnector),
		EgressResourceGroup: resourceGroup,
	})
}

const defaultNoEgressConnectorName = "whim-no-egress"

// defaultNoPublicEgressVPCCIDR and defaultNoPublicEgressSubnetCIDR let bare
// `--egress none --egress-auto-provision` work with no extra flags, matching
// the source spec's own CLI example. This range was picked to be unlikely to
// collide with a caller's existing networks; --egress-vpc-cidr/
// --egress-subnet-cidr override it for anyone who needs to avoid a
// collision Whim cannot detect on its own.
const (
	defaultNoPublicEgressVPCCIDR    = "10.242.99.0/24"
	defaultNoPublicEgressSubnetCIDR = "10.242.99.0/25"
)

func init() {
	addBuildFlags(buildCmd)
	_ = buildCmd.MarkFlagRequired("name")
	rootCmd.AddCommand(buildCmd)
}

func addBuildFlags(cmd *cobra.Command) {
	cmd.Flags().String("name", "", "image name (required; the build cache key)")
	cmd.Flags().String("egress", "public", "outbound network policy: public|none")
	cmd.Flags().String("egress-connector", "", "existing Lambda Core network connector name or ARN for --egress none")
	cmd.Flags().String("egress-connector-name", defaultNoEgressConnectorName, "Whim-managed connector name to reuse or create for --egress none")
	cmd.Flags().StringArray("egress-subnet", nil, "VPC subnet ID for creating the --egress none connector; repeat for multiple subnets")
	cmd.Flags().StringArray("egress-security-group", nil, "VPC security group ID for creating the --egress none connector; repeat for multiple groups")
	cmd.Flags().String("egress-operator-role", "", "IAM role ARN Lambda Core assumes when creating the --egress none connector")
	cmd.Flags().Bool("egress-auto-provision", false, "create or reuse a Whim-managed no-public-egress VPC and connector for --egress none")
	cmd.Flags().String("egress-resource-prefix", "", `name prefix for auto-provisioned no-public-egress resources (default "whim")`)
	cmd.Flags().Bool("egress-strict-dns", false, "block public DNS resolution under --egress none (not implemented yet)")
	cmd.Flags().String("egress-vpc-cidr", defaultNoPublicEgressVPCCIDR, "VPC CIDR block for a freshly created --egress-auto-provision resource group")
	cmd.Flags().String("egress-subnet-cidr", defaultNoPublicEgressSubnetCIDR, "subnet CIDR block for a freshly created --egress-auto-provision resource group")
	cmd.Flags().Bool("force", false, "delete and rebuild even if the image exists (destructive)")
	cmd.Flags().Bool("privileged", false, "grant all elevated OS capabilities (mount, netns, eBPF, nested containers); baked at build time")
	cmd.Flags().String("context-subdir", "", "build-context subdirectory to descend into before locating the Dockerfile")
	cmd.Flags().Bool("json", false, "print a single JSON object instead of human progress")
}

var buildCmd = &cobra.Command{
	Use:   "build <source>",
	Short: "Build a custom MicroVM image from a source context",
	Long: `whim build builds a custom MicroVM image from a build source and caches its
ARN by name for use with 'whim shell --image', 'whim run', etc.

The source is one of:

  ./dir                     a local build context (Dockerfile at its root)
  ./Dockerfile              a single local Dockerfile
  s3://bucket/key           an S3 zip archive or raw Dockerfile
  https://host/path         an HTTPS zip archive or raw Dockerfile

--egress none means NO_PUBLIC_EGRESS: direct public IPs and public hostnames
over HTTPS fail, but DNS resolution is NOT blocked (AWS security groups and
NACLs cannot filter its own DNS resolver) — this is not AWS's undocumented
NO_EGRESS. --egress-auto-provision creates or reuses a whole Whim-managed
no-public-egress VPC/connector; it needs an existing --egress-operator-role
(Whim never creates that IAM role itself).

  whim build ./app --name whim-app
  whim build ./app --name whim-app --egress none --egress-connector arn:aws:lambda:...
  whim build ./app --name whim-app --egress none --egress-auto-provision --egress-operator-role arn:aws:iam::123456789012:role/whim-network-operator
  whim build ./app --name whim-app --egress none --egress-connector-name my-connector --egress-subnet subnet-a --egress-security-group sg-a --force
  whim build s3://my-bucket/app.zip --name whim-s3 --json`,
	Args: cobra.ExactArgs(1),
	RunE: runBuild,
}

// parseEgress maps the --egress flag to a microvm.EgressMode, rejecting any
// value other than the two supported modes before any AWS call is made.
func parseEgress(s string) (microvm.EgressMode, error) {
	switch s {
	case "public":
		return microvm.EgressPublic, nil
	case "none":
		return microvm.EgressNone, nil
	default:
		return 0, fmt.Errorf("unsupported --egress %q (use 'public' or 'none')", s)
	}
}

func egressConnectorFlagsChanged(cmd *cobra.Command) bool {
	for _, name := range []string{
		"egress-connector", "egress-connector-name", "egress-subnet", "egress-security-group", "egress-operator-role",
		"egress-auto-provision", "egress-resource-prefix", "egress-strict-dns", "egress-vpc-cidr", "egress-subnet-cidr",
	} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

// validateEgressRequirement enforces --egress public's "reject every
// egress-* flag" rule, the --egress-strict-dns placeholder, and the
// "--egress none requires ..." rule — all called right after parseEgress in
// runBuild, before resolveBuildEnv's STS/bootstrap AWS calls. (This used to
// let --egress public with a connector flag reach resolveBuildEgressConnector,
// whose own identical check only runs after those AWS calls; that check
// stays in place as a defensive backstop for any other caller, but this is
// now the check that actually runs first for `whim build`.)
//
// --egress none accepts exactly three ways to specify its connector:
//   - --egress-connector: point at an existing connector by name or ARN.
//   - --egress-auto-provision: create or reuse a full Whim-managed
//     no-public-egress resource group (VPC, subnet, route table, security
//     group, connector) — see microvm.EnsureNoPublicEgressConnector.
//   - --egress-subnet and --egress-security-group with --egress-connector-name
//     explicitly set: point a caller-named connector at caller-supplied
//     subnets/security groups directly, skipping VPC creation and
//     Milestone 2's safety validation. An advanced escape hatch, not the
//     safe default — it requires explicitly naming the connector so it
//     never silently depends on the "whim-no-egress" default name.
//
// Bare "--egress none" with none of the above used to silently fall back to
// that default connector name; that is no longer accepted.
func validateEgressRequirement(cmd *cobra.Command, egress microvm.EgressMode) error {
	if egress == microvm.EgressPublic {
		if egressConnectorFlagsChanged(cmd) {
			return fmt.Errorf("--egress-* connector flags require --egress none")
		}
		return nil
	}
	if egress != microvm.EgressNone {
		return nil
	}
	if strictDNS, _ := cmd.Flags().GetBool("egress-strict-dns"); strictDNS {
		return fmt.Errorf("--egress-strict-dns is not implemented yet: DNS resolution is not blocked under --egress none")
	}
	explicitConnector, _ := cmd.Flags().GetString("egress-connector")
	autoProvision, _ := cmd.Flags().GetBool("egress-auto-provision")
	rawSubnetSG := cmd.Flags().Changed("egress-connector-name") &&
		cmd.Flags().Changed("egress-subnet") && cmd.Flags().Changed("egress-security-group")

	active := 0
	if explicitConnector != "" {
		active++
	}
	if autoProvision {
		active++
	}
	if rawSubnetSG {
		active++
	}
	switch active {
	case 0:
		return fmt.Errorf("%w: --egress none requires --egress-connector, --egress-auto-provision, or --egress-subnet/--egress-security-group with an explicit --egress-connector-name",
			microvm.ErrInvalidOption)
	case 1:
		// fall through: still need to check for flags belonging to a
		// different, unselected mode (below) before this is accepted.
	default:
		return fmt.Errorf("%w: --egress none accepts only one of --egress-connector, --egress-auto-provision, or --egress-subnet/--egress-security-group with --egress-connector-name — got more than one",
			microvm.ErrInvalidOption)
	}

	// Reject any flag that belongs to a mode other than the one just
	// selected: resolveBuildEgressConnector's chosen branch would otherwise
	// silently ignore it (e.g. --egress-connector plus --egress-vpc-cidr, or
	// --egress-auto-provision plus --egress-subnet), which is exactly the
	// ambiguity this rule exists to prevent. The primary-selector flags
	// themselves (egress-connector, egress-auto-provision, and the
	// connector-name+subnet+security-group trio) are already accounted for
	// by the active-count check above, so they're excluded here.
	var foreign []string
	switch {
	case explicitConnector != "":
		foreign = []string{
			"egress-resource-prefix", "egress-vpc-cidr", "egress-subnet-cidr",
			"egress-subnet", "egress-security-group", "egress-connector-name", "egress-operator-role",
		}
	case autoProvision:
		foreign = []string{"egress-subnet", "egress-security-group"}
	default: // rawSubnetSG
		foreign = []string{"egress-resource-prefix", "egress-vpc-cidr", "egress-subnet-cidr"}
	}
	for _, name := range foreign {
		if cmd.Flags().Changed(name) {
			return fmt.Errorf("%w: --%s is not used by the selected --egress none connector mode and would be silently ignored; remove it or switch modes",
				microvm.ErrInvalidOption, name)
		}
	}
	return nil
}

// resolveBuildEgressConnector resolves the connector ARN to use for the
// build, and — only for the --egress-auto-provision path — the managed
// resource group behind it, for persisting (Task 4.2) and --json (Task 4.3).
// Every other path returns a nil resources pointer.
func resolveBuildEgressConnector(ctx context.Context, cmd *cobra.Command, mgr *microvm.Manager, egress microvm.EgressMode) (string, *microvm.NoPublicEgressResources, error) {
	if egress == microvm.EgressPublic {
		if egressConnectorFlagsChanged(cmd) {
			return "", nil, fmt.Errorf("--egress-* connector flags require --egress none")
		}
		return "", nil, nil
	}
	if egress != microvm.EgressNone {
		return "", nil, fmt.Errorf("unsupported connector-backed egress mode %v", egress)
	}

	if autoProvision, _ := cmd.Flags().GetBool("egress-auto-provision"); autoProvision {
		prefix, _ := cmd.Flags().GetString("egress-resource-prefix")
		var connectorNameOverride string
		if cmd.Flags().Changed("egress-connector-name") {
			connectorNameOverride, _ = cmd.Flags().GetString("egress-connector-name")
		}
		operatorRole, _ := cmd.Flags().GetString("egress-operator-role")
		vpcCIDR, _ := cmd.Flags().GetString("egress-vpc-cidr")
		subnetCIDR, _ := cmd.Flags().GetString("egress-subnet-cidr")
		resources, err := mgr.EnsureNoPublicEgressConnector(ctx, microvm.NoPublicEgressSpec{
			NamePrefix:      prefix,
			ConnectorName:   connectorNameOverride,
			OperatorRoleARN: operatorRole,
			VPCCIDRBlock:    vpcCIDR,
			SubnetCIDRBlock: subnetCIDR,
		})
		if err != nil {
			return "", nil, err
		}
		return resources.ConnectorARN, resources, nil
	}

	explicit, _ := cmd.Flags().GetString("egress-connector")
	if explicit != "" {
		arn, err := mgr.EnsureNetworkConnector(ctx, microvm.NetworkConnectorSpec{Name: explicit})
		if err != nil {
			return "", nil, err
		}
		resources, err := mgr.ValidateNoPublicEgressConnector(ctx, microvm.NoPublicEgressResources{ConnectorARN: arn})
		return arn, resources, err
	}
	name, _ := cmd.Flags().GetString("egress-connector-name")
	subnets, _ := cmd.Flags().GetStringArray("egress-subnet")
	securityGroups, _ := cmd.Flags().GetStringArray("egress-security-group")
	operatorRole, _ := cmd.Flags().GetString("egress-operator-role")
	arn, err := mgr.EnsureNetworkConnector(ctx, microvm.NetworkConnectorSpec{
		Name:             name,
		SubnetIDs:        subnets,
		SecurityGroupIDs: securityGroups,
		OperatorRoleARN:  operatorRole,
		Tags: map[string]string{
			"ManagedBy": "whim",
			"Purpose":   "egress-none",
		},
	})
	if err != nil {
		return "", nil, err
	}
	resources, err := mgr.ValidateNoPublicEgressConnector(ctx, microvm.NoPublicEgressResources{
		ConnectorARN:     arn,
		SubnetIDs:        subnets,
		SecurityGroupIDs: securityGroups,
	})
	return arn, resources, err
}

func runBuild(cmd *cobra.Command, args []string) error {
	displaySource := args[0]
	source := displaySource
	name, _ := cmd.Flags().GetString("name")
	egressFlag, _ := cmd.Flags().GetString("egress")
	force, _ := cmd.Flags().GetBool("force")
	privileged, _ := cmd.Flags().GetBool("privileged")
	contextSubdir, _ := cmd.Flags().GetString("context-subdir")
	jsonOut, _ := cmd.Flags().GetBool("json")

	// Validate flags before touching AWS.
	egress, err := parseEgress(egressFlag)
	if err != nil {
		return err
	}
	if err := validateEgressRequirement(cmd, egress); err != nil {
		return err
	}

	// Lower GitHub shorthand (CLI-only) to a concrete HTTPS archive URL, and
	// carry GITHUB_TOKEN as a request header — never in the URL, output, or config.
	var httpsHeaders map[string]string
	var sourceIdentity string
	if gh, ok, gerr := parseGitHubShorthand(source); gerr != nil {
		return gerr
	} else if ok {
		source = gh.url
		httpsHeaders = githubAuthHeader(githubToken())
		if gh.immutable {
			sourceIdentity = gh.ref
		}
	}

	// Under --json, the object is the only thing on stdout: route progress
	// chatter (here and in resolveBuildEnv) to a discard writer and keep the
	// real stdout for the final object.
	out := cmd.OutOrStdout()
	if jsonOut {
		cmd.SetOut(io.Discard)
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), imageBuildTimeout)
	defer cancel()

	cfg, err := buildAWSConfig(ctx, cmd)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}

	// Shared bootstrap: caller identity, artifact bucket, build role, base image.
	env, err := resolveBuildEnv(ctx, cfg, cmd)
	if err != nil {
		return err
	}

	mgr := microvm.NewFromConfig(cfg, microvm.WithAccountID(env.accountID))
	egressConnectorARN, egressResources, err := resolveBuildEgressConnector(ctx, cmd, mgr, egress)
	if err != nil {
		return fmt.Errorf("resolve egress connector: %w", err)
	}
	opts := microvm.BuildFromSourceOptions{
		Name:               name,
		ArtifactBucket:     env.bucket,
		BaseImageARN:       env.baseImageARN,
		BuildRoleARN:       env.buildRoleARN,
		Egress:             egress,
		EgressConnectorARN: egressConnectorARN,
		Force:              force,
		ContextSubdir:      contextSubdir,
		HTTPSHeaders:       httpsHeaders,
		SourceIdentity:     sourceIdentity,
	}
	if privileged {
		opts.Capabilities = []microvm.Capability{microvm.CapabilityAll}
	}

	// Determine cache status for reporting: a non-force build over an existing
	// image reuses it rather than rebuilding.
	cached := false
	if !force {
		if _, gerr := mgr.GetImage(ctx, mgr.ImageARN(name)); gerr == nil {
			cached = true
		}
	}

	if force {
		printOut(cmd, "  Force: deleting then rebuilding image %q…\n", name)
	} else {
		printOut(cmd, "  Building image %q (this takes a few minutes if not cached)…\n", name)
	}
	arn, err := mgr.BuildFromSource(ctx, source, opts)
	if err != nil {
		return fmt.Errorf("build image: %w", err)
	}
	printOut(cmd, "  Image ready: %s\n", arn)

	// Cache the custom image ARN by name, preserving the default image_arn.
	cfgFile, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfgFile.SetImage(name, arn)
	cfgFile.SetEgress(name, egressFlag)
	cfgFile.SetEgressConnector(name, egressConnectorARN)
	var resourceGroupJSON *EgressResourceGroup
	if egressResources != nil {
		resourceGroupJSON = &EgressResourceGroup{
			VPCID:            egressResources.VPCID,
			SubnetIDs:        egressResources.SubnetIDs,
			RouteTableID:     egressResources.RouteTableID,
			RouteTableIDs:    egressResources.RouteTableIDs,
			SecurityGroupID:  egressResources.SecurityGroupID,
			SecurityGroupIDs: egressResources.SecurityGroupIDs,
			NetworkACLID:     egressResources.NetworkACLID,
			ConnectorARN:     egressResources.ConnectorARN,
			ResourceGroup:    egressResources.ResourceGroup,
		}
	}
	// SetEgressResourceGroup unconditionally, including the zero value: a
	// rebuild that no longer auto-provisions must clear a stale entry from a
	// previous build, exactly like SetEgressConnector("") does above.
	if resourceGroupJSON != nil {
		cfgFile.SetEgressResourceGroup(name, *resourceGroupJSON)
	} else {
		cfgFile.SetEgressResourceGroup(name, EgressResourceGroup{})
	}
	if err := SaveConfig(cfgFile); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	printOut(cmd, "  Saved image %q to %s\n", name, ConfigPath())

	if jsonOut {
		return renderBuildJSON(out, name, arn, displaySource, egressFlag, egressConnectorARN, cached, resourceGroupJSON)
	}
	return nil
}
