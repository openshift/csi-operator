# 5.9.5 Configuring EFS Cross Account

## 5.9.5.1 Requirements

- The EFS CSI Operator must be installed prior to beginning this procedure. Follow the _Installing the AWS EFS CSI Driver Operator_ procedure.
- Both the Red Hat OpenShift Service on AWS cluster and EFS file system must be located in the same AWS region.
- Ensure that the two VPCs referenced in this guide utilize different network CIDR ranges.

## 5.9.5.2 Prerequisites

- AWS Account A: Contains a Red Hat OpenShift Cluster 4.16 or later, deployed within a VPC
- AWS Account B: Contains a VPC (including subnets, route tables, and network connectivity). The EFS filesystem will be created in this VPC during this procedure
- OpenShift CLI (`oc`)
- AWS CLI
- `jq` command-line JSON processor

## 5.9.5.3 Environment Setup

1. Configure environment variables that will be referenced throughout this guide:

```bash
export CLUSTER_NAME="<CLUSTER_NAME>" #Cluster name of choice
export AWS_REGION="<AWS_REGION>" #AWS Region of choice
export AWS_ACCOUNT_A_ID="<ACCOUNT_A_ID>" #AWS Account B ID
export AWS_ACCOUNT_B_ID="<ACCOUNT_B_ID>" #AWS Account B ID 
export AWS_ACCOUNT_A_VPC_CIDR="<VPC_A_CIDR>" #CIDR range of VPC in Account A
export AWS_ACCOUNT_B_VPC_CIDR="<VPC_B_CIDR>" #CIDR range of VPC in Account B
export AWS_ACCOUNT_A_VPC_ID="<VPC_A_ID>" #VPC ID in Account A (cluster)
export AWS_ACCOUNT_B_VPC_ID="<VPC_B_ID>" #VPC ID in Account B (EFS cross account)
export SCRATCH_DIR="<WORKING_DIRECTORY>" #Any writeable directory of choice, used to store temporary files
export AWS_PAGER="" #Make AWS CLI output everything directly to stdout
```

2. Create the working directory:
```bash
mkdir -p $SCRATCH_DIR
```

3. Verify cluster connectivity using the `oc` command:

```bash
oc whoami
```

4. Configure AWS CLI profiles as environment variables for account switching:

```bash
export AWS_ACCOUNT_A="<ACCOUNT_A_NAME>"
export AWS_ACCOUNT_B="<ACCOUNT_B_NAME>"
```

5. Ensure that your AWS CLI is configured with JSON output format as the default for both accounts:

```bash
export AWS_DEFAULT_PROFILE=${AWS_ACCOUNT_A}
aws configure get output
export AWS_DEFAULT_PROFILE=${AWS_ACCOUNT_B}
aws configure get output
```

If the AWS CLI commands above return no value, the default output format is already set to JSON and no changes are required. If any other value is returned, reconfigure your AWS CLI to use JSON format. For detailed instructions on changing output formats, refer to: https://docs.aws.amazon.com/cli/latest/userguide/cli-usage-output-format.html

6. Unset `AWS_PROFILE` in your shell to prevent conflicts with `AWS_DEFAULT_PROFILE`:

```bash
unset AWS_PROFILE
```

## 5.9.5.4 Configure AWS Account B IAM Roles and Policies

1. Switch to your Account B profile:

```bash
export AWS_DEFAULT_PROFILE=${AWS_ACCOUNT_B}
```

2. Define the IAM Role name for the EFS CSI Driver Operator:

```bash
export ACCOUNT_B_ROLE_NAME=${CLUSTER_NAME}-cross-account-aws-efs-csi-operator
```

3. Create the IAM Trust Policy file:

```bash
cat <<EOF > $SCRATCH_DIR/AssumeRolePolicyInAccountB.json
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Principal": {
                "AWS": "arn:aws:iam::${AWS_ACCOUNT_A_ID}:root"
            },
            "Action": "sts:AssumeRole",
            "Condition": {}
        }
    ]
}
EOF
```

4. Create the IAM Role for the EFS CSI Driver Operator:

```bash
ACCOUNT_B_ROLE_ARN=$(aws iam create-role \
  --role-name "${ACCOUNT_B_ROLE_NAME}" \
  --assume-role-policy-document file://$SCRATCH_DIR/AssumeRolePolicyInAccountB.json \
  --query "Role.Arn" --output text) \
&& echo $ACCOUNT_B_ROLE_ARN
```

5. Create the IAM Policy document:

```bash
cat << EOF > $SCRATCH_DIR/EfsPolicyInAccountB.json
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "VisualEditor0",
            "Effect": "Allow",
            "Action": [
                "ec2:DescribeNetworkInterfaces",
                "ec2:DescribeSubnets"
            ],
            "Resource": "*"
        },
        {
            "Sid": "VisualEditor1",
            "Effect": "Allow",
            "Action": [
                "elasticfilesystem:DescribeMountTargets",
                "elasticfilesystem:DeleteAccessPoint",
                "elasticfilesystem:ClientMount",
                "elasticfilesystem:DescribeAccessPoints",
                "elasticfilesystem:ClientWrite",
                "elasticfilesystem:ClientRootAccess",
                "elasticfilesystem:DescribeFileSystems",
                "elasticfilesystem:CreateAccessPoint",
                "elasticfilesystem:TagResource"
            ],
            "Resource": "*"
        }
    ]
}
EOF
```

6. Create the IAM Policy:

```bash
ACCOUNT_B_POLICY_ARN=$(aws iam create-policy --policy-name "${CLUSTER_NAME}-efs-csi-policy" \
   --policy-document file://$SCRATCH_DIR/EfsPolicyInAccountB.json \
   --query 'Policy.Arn' --output text) \
&& echo ${ACCOUNT_B_POLICY_ARN}
```

7. Attach the Policy to the Role:

```bash
aws iam attach-role-policy \
   --role-name "${ACCOUNT_B_ROLE_NAME}" \
   --policy-arn "${ACCOUNT_B_POLICY_ARN}"
```

## 5.9.5.5 Configure AWS Account A IAM Roles and Policies

1. Switch to your Account A profile:
```bash
export AWS_DEFAULT_PROFILE=${AWS_ACCOUNT_A}
```

2. Create the IAM Policy document:

```bash
cat << EOF > $SCRATCH_DIR/AssumeRoleInlinePolicyPolicyInAccountA.json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "sts:AssumeRole",
      "Resource": "${ACCOUNT_B_ROLE_ARN}"
    }
  ]
}
EOF
```

3. In AWS Account A, attach the AWS-managed policy "AmazonElasticFileSystemClientFullAccess" to the OpenShift Container Platform cluster master role:

```bash
EFS_POLICY_ARN=arn:aws:iam::aws:policy/AmazonElasticFileSystemClientFullAccess
for NODE in $(oc get nodes --selector=node-role.kubernetes.io/master | tail -n +2 | awk '{print $1}')
do
   INSTANCE_PROFILE=$(aws ec2 describe-instances --filters "Name=private-dns-name,Values=${NODE}" --query 'Reservations[].Instances[].IamInstanceProfile.Arn' --output text | awk -F'/' '{print $NF}')
   MASTER_ROLE_ARN=$(aws iam get-instance-profile --instance-profile-name ${INSTANCE_PROFILE}  --query 'InstanceProfile.Roles[0].Arn' --output text)
   MASTER_ROLE_NAME=$(echo ${MASTER_ROLE_ARN} | awk -F'/' '{print $NF}')
   echo "Assigning policy ${EFS_POLICY_ARN} to role ${MASTER_ROLE_NAME} for node ${NODE}"
   aws iam attach-role-policy --role-name ${MASTER_ROLE_NAME} --policy-arn ${EFS_POLICY_ARN}
done
```

The next step depends on your cluster configuration. In both scenarios, the EFS CSI Driver Operator uses an entity to authenticate to AWS, and this entity must be granted permission to assume roles in Account B:

- If your cluster does not have STS enabled, the EFS CSI Driver Operator uses an IAM User entity for AWS authentication. Continue with the section "Attach policy to IAM User to allow role assumption".
- If your cluster has STS enabled, the EFS CSI Driver Operator uses an IAM Role entity for AWS authentication. Continue with the section "Attach policy to IAM Role to allow role assumption".

##  5.9.5.5.1 Attach policy to IAM User to allow role assumption

1. Identify the IAM User utilized by the EFS CSI Driver Operator:

```bash
EFS_CSI_DRIVER_OPERATOR_USER=$(oc -n openshift-cloud-credential-operator get credentialsrequest/openshift-aws-efs-csi-driver -o json | jq -r '.status.providerStatus.user')
```

2. Attach the Policy to the IAM User:

```bash
aws iam put-user-policy \
    --user-name "${EFS_CSI_DRIVER_OPERATOR_USER}"  \
    --policy-name efs-cross-account-inline-policy \
    --policy-document file://$SCRATCH_DIR/AssumeRoleInlinePolicyPolicyInAccountA.json
```

##  5.9.5.5.2 Attach policy to IAM Role to allow role assumption

1. Identify the IAM Role name currently utilized by the EFS CSI Driver Operator:

```bash
EFS_CSI_DRIVER_OPERATOR_ROLE=$(oc -n openshift-cluster-csi-drivers get secret/aws-efs-cloud-credentials -o jsonpath='{.data.credentials}' | base64 -d | grep role_arn | cut -d'/' -f2) && echo ${EFS_CSI_DRIVER_OPERATOR_ROLE}
```

2. Attach the Policy to the IAM Role used by the EFS CSI Driver Operator:

```bash
 aws iam put-role-policy \
    --role-name "${EFS_CSI_DRIVER_OPERATOR_ROLE}"  \
    --policy-name efs-cross-account-inline-policy \
    --policy-document file://$SCRATCH_DIR/AssumeRoleInlinePolicyPolicyInAccountA.json
```

##  5.9.5.6 Configure VPC Peering

1. Initiate a peering request from Account A to Account B:

```bash
export AWS_DEFAULT_PROFILE=${AWS_ACCOUNT_A}
PEER_REQUEST_ID=$(aws ec2 create-vpc-peering-connection --vpc-id "${AWS_ACCOUNT_A_VPC_ID}" --peer-vpc-id "${AWS_ACCOUNT_B_VPC_ID}" --peer-owner-id "${AWS_ACCOUNT_B_ID}" --query VpcPeeringConnection.VpcPeeringConnectionId --output text)
```

2. Accept the peering request from Account B:

```bash
export AWS_DEFAULT_PROFILE=${AWS_ACCOUNT_B}
aws ec2 accept-vpc-peering-connection --vpc-peering-connection-id "${PEER_REQUEST_ID}"
```

3. Retrieve the route table IDs for Account A and add routes to Account B VPC:

```bash
export AWS_DEFAULT_PROFILE=${AWS_ACCOUNT_A}
for NODE in $(oc get nodes --selector=node-role.kubernetes.io/worker | tail -n +2 | awk '{print $1}')
do
    SUBNET=$(aws ec2 describe-instances --filters "Name=private-dns-name,Values=$NODE" --query 'Reservations[*].Instances[*].NetworkInterfaces[*].SubnetId' | jq -r '.[0][0][0]')
    echo SUBNET is ${SUBNET}
    ROUTE_TABLE_ID=$(aws ec2 describe-route-tables --filters "Name=association.subnet-id,Values=${SUBNET}" --query 'RouteTables[*].RouteTableId' | jq -r '.[0]')
    echo Route table ID is $ROUTE_TABLE_ID
    aws ec2 create-route --route-table-id ${ROUTE_TABLE_ID} --destination-cidr-block ${AWS_ACCOUNT_B_VPC_CIDR} --vpc-peering-connection-id ${PEER_REQUEST_ID}
done
```

4. Retrieve the route table IDs for Account B and add routes to Account A VPC:

```bash
export AWS_DEFAULT_PROFILE=${AWS_ACCOUNT_B}
for ROUTE_TABLE_ID in $(aws ec2 describe-route-tables   --filters "Name=vpc-id,Values=${AWS_ACCOUNT_B_VPC_ID}"   --query "RouteTables[].RouteTableId" | jq -r '.[]')
do
    echo Route table ID is $ROUTE_TABLE_ID
    aws ec2 create-route --route-table-id ${ROUTE_TABLE_ID} --destination-cidr-block ${AWS_ACCOUNT_A_VPC_CIDR} --vpc-peering-connection-id ${PEER_REQUEST_ID}
done
```

##  5.9.5.7 Configure security groups in Account B to allow NFS traffic from Account A to EFS

1. Switch to your Account B profile:

```bash
export AWS_DEFAULT_PROFILE=${AWS_ACCOUNT_B}
```

2. Execute the following commands to configure the VPC security groups for EFS access:

```bash
SECURITY_GROUP_ID=$(aws ec2 describe-security-groups --filters Name=vpc-id,Values="${AWS_ACCOUNT_B_VPC_ID}" | jq -r '.SecurityGroups[].GroupId')
aws ec2 authorize-security-group-ingress \
 --group-id "${SECURITY_GROUP_ID}" \
 --protocol tcp \
 --port 2049 \
 --cidr "${AWS_ACCOUNT_A_VPC_CIDR}" | jq .
```

##  5.9.5.8 Create a region-wide EFS filesystem in Account B

1. Switch to your Account B profile:

```bash
export AWS_DEFAULT_PROFILE=${AWS_ACCOUNT_B}
```

2. Create a region-wide EFS File System:

```bash
CROSS_ACCOUNT_FS_ID=$(aws efs create-file-system --creation-token efs-token-1 \
--region ${AWS_REGION} \
--encrypted | jq -r '.FileSystemId') \
&& echo $CROSS_ACCOUNT_FS_ID
```

3. Configure region-wide Mount Targets for EFS (this creates a mount point in each subnet of your VPC):

```bash
for SUBNET in $(aws ec2 describe-subnets \
  --query 'Subnets[*].{SubnetId:SubnetId}' \
  --region ${AWS_REGION} \
  | jq -r '.[].SubnetId'); do \
    MOUNT_TARGET=$(aws efs create-mount-target --file-system-id ${CROSS_ACCOUNT_FS_ID} \
    --subnet-id ${SUBNET} \
    --region ${AWS_REGION} \
    | jq -r '.MountTargetId'); \
    echo ${MOUNT_TARGET}; \
done
```

##  5.9.5.9 Configure EFS operator for cross-account access

1. Define custom names for the secret and storage class to be created in subsequent steps:
```bash
export SECRET_NAME=my-efs-cross-account
export STORAGE_CLASS_NAME=efs-sc-cross
```

2. Create a secret that references the role ARN in Account B:
```bash
oc create secret generic ${SECRET_NAME} -n openshift-cluster-csi-drivers --from-literal=awsRoleArn="${ACCOUNT_B_ROLE_ARN}"
```

3. Grant the CSI driver controller access to the newly created secret:
```bash
oc -n openshift-cluster-csi-drivers create role access-secrets --verb=get,list,watch --resource=secrets
oc -n openshift-cluster-csi-drivers create rolebinding --role=access-secrets default-to-secrets --serviceaccount=openshift-cluster-csi-drivers:aws-efs-csi-driver-controller-sa
```

4. Create a new storage class that references the EFS ID from Account B and the secret created in the previous step:
```bash
cat << EOF | oc apply -f -
kind: StorageClass
apiVersion: storage.k8s.io/v1
metadata:
  name: ${STORAGE_CLASS_NAME}
provisioner: efs.csi.aws.com
parameters:
  provisioningMode: efs-ap
  fileSystemId: ${CROSS_ACCOUNT_FS_ID}
  directoryPerms: "700"
  gidRangeStart: "1000"
  gidRangeEnd: "2000"
  basePath: "/dynamic_provisioning"
  csi.storage.k8s.io/provisioner-secret-name: ${SECRET_NAME}
  csi.storage.k8s.io/provisioner-secret-namespace: openshift-cluster-csi-drivers
EOF
```
