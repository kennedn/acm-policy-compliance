#!/bin/bash	
set -eu
policyname="$1" 
policy_yaml=$(oc get policies -n acm-policies "${policyname}" -oyaml)
yq_expression='{
  "apiVersion": .apiVersion, 
  "kind": .kind, 
  "metadata": {
    "name": .metadata.name, 
    "namespace": .metadata.namespace
  }, 
  "compliant": .status.compliant, 
  "condition": (.status.compliancyDetails[].conditions // (.status.conditions[] | select(.type == "Compliant")))  # status field is not uniform across policy resources (naughty)
} | [""] + . | .[] | split_doc # Hack to add --- to the top of each yaml to allow collection via yq ea later on
'

# List of clusters the policy is applied to
clusters=$(yq '.status.status[].clusternamespace' <<<"${policy_yaml}")
# List of resources that are contained in the policy (ConfigurationPolicy, OperatorPolicy, etc...)
resources=$(yq -r '.spec["policy-templates"] | map (.objectDefinition | [((.kind | downcase) + "." + (.apiVersion | split("/")[-1]) + "." + (.apiVersion | split("/")[0])), .metadata.name] | join("   "))[]' <<<"${policy_yaml}")

out_yaml=$(
# For each cluster in policy
while read -r cluster; do
# For each object definition in policy
  while read -r resource name; do
  {
    # If local-cluster, we can just retrieve the policy objects status directly
    if [ "${cluster}" == "local-cluster" ]
    then
      oc get "${resource}" -n "${cluster}" "${name}" -oyaml | yq "${yq_expression}"
      exit
    fi
    # Else we must create a managedclusterview object to retrieve status of remote resource from managed cluster
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
    # Attempt to retrieve status from managedclusterview for some time
    for (( i=1; i<30; i++ )); do
      oc get managedclusterview -n "${cluster}" "${uuid}" -oyaml | yq -e ".status.result | ${yq_expression}" 2>/dev/null && break
      sleep 0.1
    done 
    # Cleanup managedclusterview objects
    oc delete managedclusterview -n "${cluster}" "${uuid}" &>/dev/null
  } &
  done <<<"${resources}"
done <<<"${clusters}"
wait
)
yq ea '[.]' <<<"${out_yaml}"
