on run argv
  -- "tab" inside the iTerm2 tell block is the tab class, not the character.
  set sep to ASCII character 9
  set nl to ASCII character 10
  set out to ""
  tell application "iTerm2"
    set wi to 0
    repeat with w in windows
      set wi to wi + 1
      set ti to 0
      repeat with t in tabs of w
        set ti to ti + 1
        repeat with s in sessions of t
          set out to out & (unique id of s) & sep & wi & sep & ti & nl
        end repeat
      end repeat
    end repeat
  end tell
  return out
end run
