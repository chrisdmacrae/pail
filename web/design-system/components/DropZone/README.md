# DropZone

The upload path: drag a folder or a .zip onto it, or press "Choose files". It turns pail while something hovers ("Let go to drop it in the pail") and shows a progress bar while uploading.

- Provide: `onFiles(files)`; `progress` (0–1) and `fileName` while uploading; `hint` only if the default ("Static files with an index.html at the top.") is wrong for the context.
- One drop zone per screen; nothing else to fill in before the drop.
