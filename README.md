
# QuantMe

**Philosophy**

Time, we believe, moves forward. All of our actions are tied to this temporal direction.

This app is designed to try to get answers to these questions:
- When do I become less productive?
- What factors affect productivity? Can I change the number of them?
- What does my typical workday look like?

And to keep in mind:
- Time is always running. Every hour/minute/second is important.
- Every hour/minute/second is a time of reflection or action.
- There is no such thing as “wasting time,” but you may or may not experience pleasure.


The logic is simple. You have a timer for 24 hours. You then decide what you want to spend your time on.
For example, you study something or write code. The app will then collect that data.
Profit. You know how much you are working that day.


## Features

- Constantly running timer
- Pomodoro is entered into a shared timer
- Add-on metrics:
  - screen time
  - logging the number of buttons pressed
  - adding custom events (e.g., drink coffee)
  - detection of different activities (listening to music, programming)

## Support

- X11 (Linux)
- #TODO Wayland (Linux)
- #TODO Other platforms and WMs



Design

The core application is daemoon running on specific socket
All new commands from CLI/TUI come to this socket

Reveal additionally:
Task system
Task bounding to the timeline
