# ACM Policy Compliance

A proof of concept Go program that replicates the compliance report gathering across managed clusters exhibited by the OpenShift ACM GUI. This is achieved by leveraging the MultiClusterView CRD.

## Usage

```bash
go run ./main.go <policy-name>
```

The program connects to the Kubernetes API using the current context. It retrieves the specified `Policy` from the `acm-policies` namespace, queries each managed cluster for the resources defined in the policy templates, and prints a YAML array summarizing the compliance results.
