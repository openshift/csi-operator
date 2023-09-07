# CSI driver operators

This repository contains code for CSI driver operators that are part of OpenShift payload and few optional ones that are installed by Operator Lifecycle Manager (OLM).

## Operators

* aws-ebs-csi-driver-operator

## Automatic generation of CSI driver assets

As part of the repository, there is generator of CSI driver YAML files in `cmd/generator`.

### Usage

`make update` will re-generate all assets automatically.

### Documentation

Some documentation is available via godoc. Usage:

```
$ godoc &
$ firefox localost:6060/pkg/github.com/openshift/csi-operator/
```

Good starting points are `pkg/generator` and `pkg/generated-assets`.
