# Linux release dependency: musl and static libraries avoid a host glibc ABI.
FROM alpine:3.22 AS build
RUN apk add --no-cache build-base bison pkgconf curl libevent-dev libevent-static \
    ncurses-dev ncurses-static ncurses-terminfo
WORKDIR /build
RUN curl --fail --location --retry 3 -o tmux.tar.gz \
      https://github.com/tmux/tmux/releases/download/3.8/tmux-3.8.tar.gz \
    && echo 'e79c699c7e949dccd0a4a125e17b8d1261e311b16979dbc8eb34542f3966d82e  tmux.tar.gz' | sha256sum -c - \
    && tar xzf tmux.tar.gz
WORKDIR /build/tmux-3.8
RUN ./configure --enable-static --disable-utf8proc --disable-systemd \
      --disable-utempter --with-TERM=tmux-256color \
    && make -j2 \
    && test "$(./tmux -V)" = 'tmux 3.8' \
    && ! readelf -l tmux | grep -q INTERP \
    && ! readelf -d tmux | grep -q NEEDED \
    && mkdir -p /out/bin /out/terminfo /out/licenses \
    && cp tmux /out/bin/tmux \
    && strip /out/bin/tmux \
    && cp -rL /usr/share/terminfo/. /out/terminfo/ \
    && apk info -v > /out/build-packages.txt \
    && printf 'tmux 3.8\n' > /out/version.txt
COPY third_party_licenses/ /out/licenses/
RUN tar -C /out -czf /tmux.tar.gz bin terminfo licenses build-packages.txt version.txt
FROM scratch AS export
COPY --from=build /tmux.tar.gz /tmux.tar.gz
