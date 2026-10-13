# Dashboard screenshots

These images show the current NVKR renderer in an 80x24 monochrome simulation
terminal using the checked-in `.github/nvkr-run/plan.json`. The Plan is loaded
and validated by `planview.Load`; `graphDisplay` supplies the same typed
description as `run graph --ui=tui`. Enter and Esc events drive the actual
`runpresenter.View`, and `tcell.SimulationScreen.GetContents` captures its cells.
The cells are rasterized with Menlo without changing their text or layout.

This is controlled presentation evidence, not producer execution, release
verification or a human usability study. The fixed snapshot contains 35 named
outputs and 1051 total entries. No transport references or private paths are
included in the projection.

![Named output overview](overview.png)

![Selected output details and its dependency](details.png)

![Drilling into the output artifact with exact Back navigation](drilldown.png)

![Close confirmation with Stay selected by default](quit.png)
