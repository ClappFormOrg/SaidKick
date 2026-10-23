# sAId-Kick Appstore — Helm Charts

Two charts ship with the platform:

| Chart | Purpose | Cluster |
|-------|---------|---------|
| [`saidkick-hub`](saidkick-hub/README.md) | API + Webapp + PostgreSQL + OpenBao + VictoriaMetrics | Hub / orchestration cluster |
| [`saidkick-agent`](saidkick-agent/README.md) | Agent + ESO + Argo CD spoke | Each target (spoke) cluster |

## Hub → Spoke flow

```
1.  Install saidkick-hub on the hub cluster.
2.  Initialise OpenBao (unseal, enable KV v2 + PKI, create service token).
3.  Run helm upgrade to inject vault credentials into the hub.
4.  POST /api/v1/clusters  →  hub returns a helm install command.
5.  Run that command on the spoke cluster  →  installs saidkick-agent.
6.  Agent performs handshake, receives mTLS cert, cluster → "ready".
7.  Deploy apps via POST /api/v1/deployments.
```

See individual chart READMEs for detailed setup instructions.
