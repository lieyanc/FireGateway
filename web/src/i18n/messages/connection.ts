import { defineMessages } from "@/i18n/define"

export const connection = defineMessages({
  en: {
    title: "Node connections",
    description:
      "Configure this gateway here, then open the other gateway and configure its side of the pair. Save and restart each node before pairing.",
    enabled: "Enable peer replication and failover",
    identity: "Node identities",
    identityHint:
      "Use the same cluster ID and initial writer on both gateways, and swap the local and peer identities.",
    locked:
      "Node identities and the initial writer are fixed while this cluster is enabled. Connection addresses and credentials can still be changed.",
    nodeId: "This node ID",
    id: "Cluster ID",
    peerId: "Peer node ID",
    initialWriter: "Initial primary node ID",
    writerHint:
      "Enter one of the two node IDs. The other node is the automatic backup.",
    peer: "Peer connection",
    peerHint:
      "Rules synchronize directly between the gateways. LAN addresses identify the destinations used by the upstream router.",
    address: "This node LAN IPv4",
    peerAddress: "Peer LAN IPv4",
    peerUrl: "Peer management URL",
    peerToken: "Shared peer secret",
    tokenHint:
      "Use the same random secret of at least 32 characters on both nodes. Change it on both nodes together.",
    peerCaFile: "Peer CA certificate path (optional)",
    caHint: "Path on this gateway. Leave empty to use the system trust store.",
    allowHttpPeer: "Allow HTTP on an isolated management network",
    upstream: "Upstream OpenWrt",
    upstreamHint:
      "Use the router’s existing API and named forwarding rules. Saving or testing this form does not change the router.",
    routerUrl: "OpenWrt ubus URL",
    username: "Router username",
    password: "Router password",
    caFile: "Router CA certificate path (optional)",
    redirects: "Managed forwarding sections",
    redirectsHint:
      "Existing UCI section names, separated by commas or spaces; for example dmz. Use the same sections on both nodes.",
    pollInterval: "Heartbeat interval (seconds)",
    failoverAfter: "Failover delay (seconds)",
    keepSecret: "Configured; leave empty to keep",
    save: "Save connection settings",
    saved: "Connection settings saved",
    pending:
      "Saved settings are waiting for a restart. The status below still describes the running configuration.",
    test: "Test saved peer connection",
    testHint:
      "Save changes first. The peer must already be running with matching settings. This checks authentication and identity without pairing or copying rules.",
    testSuccess: "Peer connection, authentication and node identity verified",
  },
  zh: {
    title: "节点连接配置",
    description:
      "在此配置本机，再打开另一台网关配置对端。两台节点分别保存并重启后，即可配对。",
    enabled: "启用节点同步与主备切换",
    identity: "节点身份",
    identityHint:
      "两台网关使用相同的集群 ID 和初始主节点 ID，本机与对端身份互换。",
    locked: "集群启用后，节点身份与初始主节点固定；仍可修改连接地址和凭据。",
    nodeId: "本机节点 ID",
    id: "集群 ID",
    peerId: "对端节点 ID",
    initialWriter: "初始主节点 ID",
    writerHint: "填写两台节点之一的 ID，另一台作为自动接管的备用机。",
    peer: "对端连接",
    peerHint: "规则直接在网关之间同步。LAN 地址用于上游路由器转发目标。",
    address: "本机 LAN IPv4",
    peerAddress: "对端 LAN IPv4",
    peerUrl: "对端管理地址",
    peerToken: "节点共享密钥",
    tokenHint:
      "两台节点使用相同的随机密钥，至少 32 个字符；更换时需要同步修改两台节点。",
    peerCaFile: "对端 CA 证书路径（可选）",
    caHint: "填写本机上的证书路径；留空则使用系统信任的证书。",
    allowHttpPeer: "允许在隔离管理网络中使用 HTTP",
    upstream: "上游 OpenWrt",
    upstreamHint:
      "使用路由器已有 API 和已命名的转发规则。保存或测试此表单不会修改路由器。",
    routerUrl: "OpenWrt ubus 地址",
    username: "路由器用户名",
    password: "路由器密码",
    caFile: "路由器 CA 证书路径（可选）",
    redirects: "受管转发规则名称",
    redirectsHint:
      "填写已有 UCI 配置段名称，多个名称用逗号或空格分隔，例如 dmz；两台节点需填写相同名称。",
    pollInterval: "心跳间隔（秒）",
    failoverAfter: "故障接管等待（秒）",
    keepSecret: "已配置，留空保留",
    save: "保存连接配置",
    saved: "连接配置已保存",
    pending: "新配置已保存，重启后生效。下方运行状态仍对应当前正在使用的配置。",
    test: "测试已保存的对端连接",
    testHint:
      "请先保存修改。对端需已运行匹配的配置；测试仅验证连接、认证和节点身份，不执行配对或规则复制。",
    testSuccess: "对端连接、认证及节点身份验证通过",
  },
})
