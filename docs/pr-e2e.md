# Run E2E tests for your pull request

Run the full NVKR build and VM E2E on your PR branch before merging. Contributors
can run it in their own fork; write access to `dsx-ai-factory/nvkr` is not required.
The test runs on a public GitHub-hosted AMD64 runner and needs no NVIDIA runners,
repository secrets or registry credentials. Build outputs stay on the disposable
runner; they are not uploaded or published.

The workflow builds latest NVKE `main` using your NVKR changes, boots a site VM,
creates a tenant, checks readiness, workload and DNS, then cleans up. See the
[nightly guide](nvke-nightly.md) for the topology, timeouts and validation limits.

## Push your PR branch

Push your changes to the branch in your fork that the PR uses. In the commands
below, replace `YOUR_GITHUB_USERNAME/nvkr` with your fork and `YOUR_PR_BRANCH`
with that branch's name.

The branch must include the public-hosted E2E version of
`.github/workflows/nvkr-nightly.yml` described in the nightly guide. Update older
branches from upstream `main` after that workflow change is merged.

## Enable Actions in your fork

Open **your fork** on GitHub, select **Actions**, and enable workflows if GitHub
prompts you. Select **NVKR nightly NVKE end-to-end tests**; if that workflow is
disabled, click **Enable workflow**.

The workflow file must also exist on your fork's default branch for GitHub to
offer manual dispatch. If your fork is old, sync its default branch with upstream
before continuing. GitHub documents these requirements under
[manual workflow runs](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow).

## Trigger E2E on your branch

With GitHub CLI authenticated to your account:

```sh
gh workflow run nvkr-nightly.yml \
  --repo YOUR_GITHUB_USERNAME/nvkr \
  --ref YOUR_PR_BRANCH
```

Or, in **your fork's Actions tab**, select **NVKR nightly NVKE end-to-end tests →
Run workflow**, choose **your PR branch** in the branch dropdown, then click
**Run workflow**. No workflow inputs are required. Forks support manual dispatch;
scheduled jobs run only in `dsx-ai-factory/nvkr`.

The selected branch supplies both the workflow and NVKR code. Select the same
branch your PR uses; the run starts when you dispatch it.

If your PR branch is in the upstream repository and you have write access there,
use `--repo dsx-ai-factory/nvkr` instead. Use the same repository for every command
below.

## Check the result and share it on your PR

Open the URL returned by the dispatch, or find your run:

```sh
gh run list --repo YOUR_GITHUB_USERNAME/nvkr \
  --workflow nvkr-nightly.yml --branch YOUR_PR_BRANCH \
  --event workflow_dispatch \
  --json databaseId,headSha,status,conclusion,url
```

Choose the run you just started. Check that `headSha` is the PR commit you want
to test. Using its numeric `databaseId` as `RUN_ID`:

```sh
gh run watch RUN_ID --repo YOUR_GITHUB_USERNAME/nvkr --exit-status
gh run view RUN_ID --repo YOUR_GITHUB_USERNAME/nvkr \
  --json headSha,status,conclusion,url
```

A completed run with `conclusion: success` passes the full build/site/tenant
test and cleanup. Pending or ordinary PR CI results do not establish that result.
The run summary records the NVKR and NVKE commits used. Paste the run URL and
tested NVKR commit into your PR so reviewers can inspect the result.

For a failure, open the failed step in Actions or inspect its logs:

```sh
gh run view RUN_ID --repo YOUR_GITHUB_USERNAME/nvkr --log-failed
```

After fixing and pushing changes to your PR branch, run the dispatch command
again. GitHub's **Re-run jobs** keeps the
[original NVKR commit](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs),
so it does not test your newly pushed fix.
