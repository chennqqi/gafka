package command

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/funkygao/gafka/ctx"
	"github.com/funkygao/gafka/zk"
	"github.com/funkygao/gocli"
	"github.com/funkygao/golib/color"
	"github.com/funkygao/golib/pipestream"
	consulapi "github.com/hashicorp/consul/api"
	"github.com/ryanuber/columnize"
)

// consul members will include:
// - zk cluster as server
// - agents
//   - brokers
//   - kateway
type Members struct {
	Ui  cli.Ui
	Cmd string

	brokerHosts, zkHosts, katewayHosts map[string]struct{}
	nodeHostMap                        map[string]string // consul members node->ip
}

func (this *Members) Run(args []string) (exitCode int) {
	var (
		zone        string
		showLoadAvg bool
		exec        string
		node        string
		role        string
	)
	cmdFlags := flag.NewFlagSet("members", flag.ContinueOnError)
	cmdFlags.Usage = func() { this.Ui.Output(this.Help()) }
	cmdFlags.StringVar(&zone, "z", ctx.ZkDefaultZone(), "")
	cmdFlags.BoolVar(&showLoadAvg, "l", false, "")
	cmdFlags.StringVar(&exec, "exec", "", "")
	cmdFlags.StringVar(&node, "n", "", "")
	cmdFlags.StringVar(&role, "r", "", "")
	if err := cmdFlags.Parse(args); err != nil {
		return 1
	}

	if validateArgs(this, this.Ui).
		requireAdminRights("-exec").
		invalid(args) {
		return 2
	}

	zkzone := zk.NewZkZone(zk.DefaultConfig(zone, ctx.ZoneZkAddrs(zone)))
	this.fetchAllRunningHostsFromZk(zkzone)

	members := this.consulMembers()
	consulLiveNode := make([]string, 0, len(members))
	for _, member := range members {
		if member.Status == 1 {
			consulLiveNode = append(consulLiveNode, member.Addr)
		} else {
			this.Ui.Warn(fmt.Sprintf("%s %s status:%d", member.Name, member.Addr, member.Status))
		}
	}

	consulLiveMap := make(map[string]struct{})
	brokerN, zkN, katewayN, unknownN := 0, 0, 0, 0
	for _, node := range consulLiveNode {
		_, presentInBroker := this.brokerHosts[node]
		_, presentInZk := this.zkHosts[node]
		_, presentInKateway := this.katewayHosts[node]
		if presentInBroker {
			brokerN++
		}
		if presentInZk {
			zkN++
		}
		if presentInKateway {
			katewayN++
		}

		if !presentInBroker && !presentInZk && !presentInKateway {
			unknownN++
		}

		consulLiveMap[node] = struct{}{}
	}

	// all brokers should run consul
	for broker, _ := range this.brokerHosts {
		if _, present := consulLiveMap[broker]; !present {
			this.Ui.Warn(fmt.Sprintf("- %s", broker))
		}
	}

	switch {
	case showLoadAvg:
		this.displayLoadAvg(role)

	case exec != "":
		this.executeOnAll(exec, role, node)

	default:
		this.displayMembers(members, role)
	}

	// summary
	this.Ui.Output(fmt.Sprintf("Zk:%d Broker:%d Kateway:%d ?:%s => %d",
		zkN, brokerN, katewayN, color.Yellow("%d", unknownN), zkN+brokerN+katewayN+unknownN))

	return
}

func (this *Members) fetchAllRunningHostsFromZk(zkzone *zk.ZkZone) {
	this.brokerHosts = make(map[string]struct{})
	zkzone.ForSortedBrokers(func(cluster string, brokers map[string]*zk.BrokerZnode) {
		for _, brokerInfo := range brokers {
			this.brokerHosts[brokerInfo.Host] = struct{}{}
		}
	})

	this.zkHosts = make(map[string]struct{})
	for _, addr := range zkzone.ZkAddrList() {
		zkNode, _, err := net.SplitHostPort(addr)
		swallow(err)
		this.zkHosts[zkNode] = struct{}{}
	}

	this.katewayHosts = make(map[string]struct{})
	kws, err := zkzone.KatewayInfos()
	swallow(err)
	for _, kw := range kws {
		host, _, err := net.SplitHostPort(kw.PubAddr)
		swallow(err)
		this.katewayHosts[host] = struct{}{}
	}
}

// TODO role is ignored
func (this *Members) executeOnAll(execCmd string, role string, node string) {
	args := []string{"exec"}
	if node != "" {
		args = append(args, fmt.Sprintf("-node=%s", node))
	}
	args = append(args, strings.Split(execCmd, " ")...)
	cmd := pipestream.New("consul", args...)
	err := cmd.Open()
	swallow(err)
	defer cmd.Close()

	this.Ui.Info(fmt.Sprintf("%s ...", execCmd))

	lines := make([]string, 0)
	header := "Node|Host|Role|Result"
	lines = append(lines, header)

	scanner := bufio.NewScanner(cmd.Reader())
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "finished with exit code 0") ||
			strings.Contains(line, "completed / acknowledged") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 1 {
			continue
		}

		node := fields[0]
		if strings.HasSuffix(node, ":") {
			node = strings.TrimRight(node, ":")
		}

		host := this.nodeHostMap[node]
		lines = append(lines, fmt.Sprintf("%s|%s|%s|%s", node, host, this.roleOfHost(host),
			strings.Join(fields[1:], " ")))
	}

	if len(lines) > 1 {
		sort.Strings(lines[1:])
		this.Ui.Output(columnize.SimpleFormat(lines))
	}
}

func (this *Members) displayMembers(members []*consulapi.AgentMember, role string) {
	lines := make([]string, 0)
	header := "Node|Host|Role"
	lines = append(lines, header)
	for _, member := range members {
		hostRole := this.roleOfHost(member.Addr)
		if role != "" && role != hostRole {
			continue
		}

		lines = append(lines, fmt.Sprintf("%s|%s|%s", member.Name, member.Addr, hostRole))
	}

	if len(lines) > 1 {
		sort.Strings(lines[1:])
		this.Ui.Output(columnize.SimpleFormat(lines))
	}
}

func (this *Members) displayLoadAvg(role string) {
	cmd := pipestream.New("consul", "exec",
		"uptime", "|", "grep", "load")
	err := cmd.Open()
	swallow(err)
	defer cmd.Close()

	lines := make([]string, 0)
	header := "Node|Host|Role|Load Avg"
	lines = append(lines, header)

	scanner := bufio.NewScanner(cmd.Reader())
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		node := fields[0]
		parts := strings.Split(line, "load average:")
		if len(parts) < 2 {
			continue
		}
		if strings.HasSuffix(node, ":") {
			node = strings.TrimRight(node, ":")
		}

		loadAvg := strings.TrimSpace(parts[1])
		if loadAvg[0] > '0' {
			loadAvg += " !"
			if loadAvg[0] > '2' {
				loadAvg += "!"
			}
			if loadAvg[0] > '4' {
				loadAvg += "!"
			}
		}

		host := this.nodeHostMap[node]
		hostRole := this.roleOfHost(host)
		if role != "" && hostRole != role {
			continue
		}

		lines = append(lines, fmt.Sprintf("%s|%s|%s|%s", node, host, hostRole, loadAvg))
	}

	if len(lines) > 1 {
		sort.Strings(lines[1:])
		this.Ui.Output(columnize.SimpleFormat(lines))
	}
}

func (this *Members) roleOfHost(host string) string {
	if _, present := this.brokerHosts[host]; present {
		return "B"
	}
	if _, present := this.zkHosts[host]; present {
		return "Z"
	}
	if _, present := this.katewayHosts[host]; present {
		return "K"
	}
	return "?"
}

func (this *Members) consulMembers() []*consulapi.AgentMember {
	cf := consulapi.DefaultConfig()
	client, err := consulapi.NewClient(cf)
	swallow(err)
	members, err := client.Agent().Members(false)
	swallow(err)

	m := make(map[string]*consulapi.AgentMember, len(members))
	sortedName := make([]string, 0, len(members))
	this.nodeHostMap = make(map[string]string, len(members))
	for _, member := range members {
		m[member.Name] = member
		this.nodeHostMap[member.Name] = member.Addr

		sortedName = append(sortedName, member.Name)
	}
	sort.Strings(sortedName)

	r := make([]*consulapi.AgentMember, 0, len(sortedName))
	for _, name := range sortedName {
		r = append(r, m[name])
	}

	return r
}

func (*Members) Synopsis() string {
	return "Verify consul members match kafka zone"
}

func (this *Members) Help() string {
	help := fmt.Sprintf(`
Usage: %s members [options]

    Verify consul members match kafka zone

    -z zone

    -r role
      Available roles: [Z|K|B]

    -l
      Display each member load average

    -exec <cmd>
      Execute cmd on all members and print the result by host
      e,g. gk members -exec "ifconfig bond0 | grep 'TX bytes'"

    -n node
      Execute cmd on a single node

`, this.Cmd)
	return strings.TrimSpace(help)
}
