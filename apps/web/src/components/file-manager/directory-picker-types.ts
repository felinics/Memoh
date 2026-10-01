/** One child directory in the folder picker; `path` is absolute on its target. */
export interface PickerDirectory {
  name: string
  path: string
}

/** A listed directory: its resolved absolute path and its child directories. */
export interface DirectoryListing {
  path: string
  directories: PickerDirectory[]
}
