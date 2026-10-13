# Recorded Plans

`nvke-m2.json` preserves the exact canonical bytes of `.github/nvkr-run/plan.json`
from commit `540c5d3d8b8198ddd85450e413097fab0a69661b`. Tests use its embedded M2
snapshots for offline contract coverage independently of the currently selected
generated workflow. This fixture is not evidence of an operational Run, successful
external build, or release qualification.

To recover the original bytes, run from the repository root:

```sh
git show 540c5d3d8b8198ddd85450e413097fab0a69661b:.github/nvkr-run/plan.json > testdata/plans/nvke-m2.json
```

Do not hand-edit or reformat this generated fixture.
