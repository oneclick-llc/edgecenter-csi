# Edgecenter CSI driver for Kubernetes

### About

This driver allows Kubernetes to access [Edgecenter Disks](https://edgecenter.ru/knowledge-base/cloud/volumes) volume, csi plugin name: `csi.edgecenter.org`, supported accessModes: `ReadWriteOnce`

## Kubernetes installation

### Requirements

* Kubernetes 1.31+
* Kubernetes has to allow privileged containers

### Manual installation

#### 1. Create a config map and secret

The controller creates one API client for each region. The region topology must match the region label on each node.

```yaml
kind: Secret
apiVersion: v1
metadata:
  name: csi-ec-plugin-config
  namespace: kube-system
type: Opaque
stringData:
  config.yaml: |
    apiUrl: https://api.edgecenter.ru/cloud
    projectID: __PROJECT_ID__
    apiToken: __API_TOKEN__
    regions:
      - name: moscow
        regionID: 10
        topology: moscow
      - name: moscow-2
        regionID: 112
        topology: moscow-2
      - name: moscow-3
        regionID: 116
        topology: moscow-3
---
kind: ConfigMap
apiVersion: v1
metadata:
   name: csi-ec-plugin-config
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

#### 3. Deploy the driver

```bash
cd deploy/manifests
for i in $(ls *.yaml);do kubectl apply -f $i;done
```

The sample node plugin targets Moscow. To add another region, copy `nodeplugin.yaml` once for that region.
Set a unique DaemonSet name, the node selector, and `CSI_TOPOLOGY_REGION` to match the region label.
Keep `CSI_DRIVER_NAME` equal to `csi.edgecenter.org` in every copy.

#### 4. Test the CSI driver

1. Create a pvc using the new storage class:

    ```bash
    kubectl create -f examples/pvc.yaml
    ```

2. Check if the PVC has been bound:

    ```bash
    $ kubectl get pvc test-pvc
    NAME         STATUS    VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS   AGE
    test-pvc     Bound     pvc-c5d4634f-8507-11e8-9f33-0e243832354b   5Gi        RWO            csi-ec-hiiops  9s
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

