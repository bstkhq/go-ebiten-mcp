# An X server, and nothing else.
#
# The game does not run in here. It runs wherever you build it — your machine,
# your own build image — and connects to this one over the socket in
# /tmp/.X11-unix. That split is the point: a container that also compiled or ran
# the game would link it against one set of libraries and run it against
# another, and this way there is nothing of yours in here to mismatch.
#
# Weston covers both modes with one flag. --renderer=gl renders on the GPU
# through /dev/dri/renderD128, needing no privileges and no DRM master;
# --renderer=pixman needs no GPU at all and Xwayland above it falls back to
# llvmpipe. Xvfb would only do the second, so there is no reason to carry both.
#
# This file is embedded in the ebitenmcp binary and built on demand, so there is
# no image to publish and nothing to pull. Editing it changes the tag and
# rebuilds by itself.
FROM docker.io/library/archlinux:latest

RUN pacman -Sy --noconfirm --needed \
        weston xorg-xwayland \
        mesa libglvnd mesa-utils \
        libx11 libxkbcommon libxcursor \
    && pacman -Scc --noconfirm

# Not under /tmp: that directory is shared with the host so the X socket can be
# reached, and weston's runtime state has no business going there.
ENV XDG_RUNTIME_DIR=/run/xdg
