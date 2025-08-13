#!/bin/bash	
policyname="$1" 
policy_yaml=$(oc get policies -n acm-policies "${policyname}" -oyaml)
yq_expression='{"apiVersion": .apiVersion, "kind": .kind, "metadata": {"name": .metadata.name, "namespace": .metadata.namespace}, "compliant": .status.compliant, "condition": (.status.compliancyDetails[].conditions // (.status.conditions[] | select(.type == "Compliant"))) } | [""] + . | .[] | split_doc'

out_yaml=$(
# For each cluster in policy
yq '.status.status[].clusternamespace' <<<"${policy_yaml}" | while read -r cluster; do
# For each object definition in policy
  yq -P -ojson <<<"${policy_yaml}" | jq -r '.spec["policy-templates"] | map (.objectDefinition | ["\(.kind | ascii_downcase).\(.apiVersion | split("/")[-1]).\(.apiVersion | split("/")[0])", .metadata.name] | join(" "))[]' | while read -r resource name; do
  {
    if [ "${cluster}" == "local-cluster" ]
    then
      oc get "${resource}" -n "${cluster}" "${name}" -oyaml | yq "${yq_expression}"
      exit
    fi
    uuid=$(openssl rand -hex 20) 
    oc create -f - &>/dev/null <<-EOF
      apiVersion: view.open-cluster-management.io/v1beta1
      kind: ManagedClusterView
      metadata:
        name: ${uuid}
        namespace: ${cluster}
        labels:
          viewName: ${uuid}
      spec:
        scope:
          name: ${name}
          resource: ${resource}
          namespace: ${cluster}
EOF
    oc get managedclusterview -n "${cluster}" "${uuid}" -oyaml | yq ".status.result | ${yq_expression}"
    oc delete managedclusterview -n "${cluster}" "${uuid}" &>/dev/null
  } &
  done
done
wait
)
yq ea '[.]' <<<"${out_yaml}"

