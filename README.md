# Edgecenter CSI driver for Kubernetes

### About

This driver allows Kubernetes to access [Edgecenter Disks](https://edgecenter.ru/knowledge-base/cloud/volumes) volume, csi plugin name: `csi.edgecenter.org`, supported accessModes: `ReadWriteOnce`

## Kubernetes installation

### Requirements

* Kubernetes 1.31+
* Kubernetes has to allow privileged containers

### Manual installation

#### 1. Create a config map and secret

The controller creates one API client for each region. Each topology value must match the region label on its nodes.

```yaml
kind: Secret
apiVersion: v1
metadata:
  name: ec-csi-plugin-config
  namespace: kube-system
type: Opaque
stringData:
  config.yaml: |
    apiUrl: __CLOUD_API_URL__
    projectID: __PROJECT_ID__
    # Keep this value set to the region used by existing volume handles.
    regionID: 10
    apiToken: __API_TOKEN__
    regions:
      - regionID: 10
        topology: moscow
      - regionID: 112
        topology: moscow-2
      - regionID: 116
        topology: moscow-3
---
kind: ConfigMap
apiVersion: v1
metadata:
   name: ec-csi-plugin-config
   namespace: kube-system
data:
   CLUSTER_ID: "__K8S_CLUSTER_ID__"
```

#### 2. Create one StorageClass for each region

Use the same provisioner name in every class. Set the region ID and topology for that class.

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: ec-moscow-standard
provisioner: csi.edgecenter.org
volumeBindingMode: WaitForFirstConsumer
parameters:
  type: standard
  csi.edgecenter.org/region-id: "10"
allowedTopologies:
  - matchLabelExpressions:
      - key: topology.kubernetes.io/region
        values:
          - moscow
```

The `region-id` value must match one entry in `regions`. The topology value must match the region label on the node.

Keep the top-level `regionID` when you upgrade an existing single-region setup. The driver uses it to route old, unprefixed volume IDs.

Set the top-level `regionID` to one configured region when you list multiple regions. The driver uses it to route old volume IDs after the upgrade.

New volume handles include the EdgeCenter region ID. The driver routes old handles through the top-level `regionID`.

#### 3. Deploy the driver

```bash
cd deploy/manifests
for i in $(ls *.yaml);do kubectl apply -f $i;done
```

The sample node plugin targets Moscow. To add a region, make one node plugin DaemonSet for that region.
Set a unique DaemonSet name, select nodes by region label, and set `CSI_TOPOLOGY_REGION` to that label.
Keep `CSI_DRIVER_NAME` equal to `csi.edgecenter.org` in every DaemonSet.

#### 4. Test the CSI driver

1. Create a pvc using the new storage class:

    ```bash
    kubectl create -f examples/pvc.yaml
    ```

2. Check if the PVC has been bound:

    ```bash
    $ kubectl get pvc test-pvc
    NAME         STATUS    VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS        AGE
    test-pvc     Bound     pvc-c5d4634f-8507-11e8-9f33-0e243832354b   5Gi        RWO            ec-moscow-standard  9s
    ```

3. Create a test pod which mounts your volume:

    ```bash
    kubectl create -f examples/pod.yaml
    ```

   If the pod can start, everything should be working.

## Development

This project can be built like any other go application.

```bash
go get -u github.com/Edge-Center/edgecenter-csi
```

