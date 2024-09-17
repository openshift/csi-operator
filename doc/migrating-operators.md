# Process of moving operators to csi-operator monorepo

> **Note**
>
> This doc previously lived in [openshift/enhancements][enhancements].
>
> [enhancements]: https://raw.githubusercontent.com/openshift/enhancements/master/enhancements/storage/csi-driver-operator-merge.md

We have come with following flow for moving operators from their own repository into this repo.

## Avoid `.gitignore` related footguns

Remove any `.gitignore` entries in the source repository that would match a directory / file that we need.
For example, `azure-disk-csi-driver-operator` in `.gitignore` matched `cmd/azure-disk-csi-driver-operator` directory that we really need not to be ignored.
See https://github.com/openshift/csi-operator/pull/110, where we had to fix after merge to `csi-operator`.

## Move existing code into csi-operator repository

Using `git-subtree`, move your existing operator code to https://github.com/openshift/csi-operator/tree/master/legacy

```
git subtree add --prefix legacy/azure-disk-csi-driver-operator https://github.com/openshift/azure-disk-csi-driver-operator.git master --squash
```

Once this has been done, all changes should now take place in `csi-operator`.
You can sync back to the original repository like so:

```
git subtree push --prefix legacy/azure-disk-csi-driver-operator https://github.com/openshift/azure-disk-csi-driver-operator.git master
```

## Add a `Dockerfile` for building images from new location

Place a `Dockerfile.<operator>` at top of csi-operator tree and make sure that you are able to build an image of the operator from csi-operator repository.

## Update `openshift/release` to build image from new location

Make a PR to [openshift/release](https://github.com/openshift/release) repository to build the operator from `csi-operator`.
For example, https://github.com/openshift/release/pull/46233

1. Update also `storage-conf-csi-<operator>-commands.sh`, the test manifest will be at a different location.
2. Make sure that rehearse jobs for both older versions of operator and newer versions of operator pass.

## Update `openshift/ocp-build-data` to ship image from new location

Make a PR to [openshift/ocp-build-data](https://github.com/openshift-eng/ocp-build-data) repository to change location of the image.
For example, https://github.com/openshift-eng/ocp-build-data/pull/4148

1. Notice the `cachito` line in the PR - we need to build with the vendor from legacy/ directory.
2. Ask ART for a scratch build. Make sure you can install a cluster with that build.

```
oc adm release new \
--from-release=registry.ci.openshift.org/ocp/release:4.15.0-0.nightly.XYZ \
azure-disk-csi-driver-operator=<the scratch build> \
--to-image=quay.io/jsafrane/scratch:release1 \
--name=4.15.0-0.nightly.jsafrane.1

oc adm release extract --command openshift-install quay.io/jsafrane/scratch:release1
```

This step is only applicable for CVO based operators and not OLM based operators.
For OLM based operator - either an image can be built locally and deployed using your personal index image or you can ask ART team for a scratch image when you open `ocp-build-data` PR and proceed to include that image in your personal index image.

## Co-ordinating merges in `openshift/ocp-build-data` and `openshift/release` repository

Both PRs in `openshift/release` and `openshift/ocp-build-data` must be merged +/- at the same time.
There is a robot that syncs some data from the latter to the former and actually breaks things when these two repos use different source repository to build images.

## Enjoy the build from `openshift/csi-operator` repository

After aforementioned changes, your new operator should be able to be built from `openshift/csi-operator` repo and everything should work.

## Moving operator to new structure in `openshift/csi-operator`

So in previous section we merely copied existing code from operator’s own repository into `csi-operator` repository. We did not change anything.

But once your operator has been changed to conform to new code in `openshift/csi-operator` repo, You need to perform following additional steps:

1. Make sure that `Dockerfile.<operator>` at top of the `csi-operator` tree refers to new location of code and not older `legacy/<operator>` location. See example of existing Dockerfiles.
2. After your changes to `csi-operator` are merged, you should remove the old location from cachito - https://github.com/openshift-eng/ocp-build-data/pull/4219

Please note the changes to `ocp-build-data`should be merged almost same time as changes into `csi-operator`'s Dockerfile are merged, otherwise we risk builds from breaking.

## Post migration changes

Once migration is complete, we should perform following post migration steps to ensure that we are not left over with legacy stuff:

1. Mark existing `openshift/<vendor>-<driver>-operator` repository as deprecated.
2. Ensure that we have test manifest available in `test/e2e` directory.
3. Make changes into `release` repository so as it longer relies on anything from `legacy` directory. See - https://github.com/openshift/release/pull/49655 for example.
4. Remove code from `vendor/legacy` in `csi-operator` repository.
