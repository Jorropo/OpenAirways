const rl = @import("raylib");
const std = @import("std");
const print = std.debug.print;

const Thread = std.Thread;
const Allocator = std.mem.Allocator;
const Child = std.process.Child;
const ArrayList = std.ArrayList;

const screen_w = 1280;
const screen_h = 720;

const canvas_w: f32 = 960;
const canvas_h: f32 = 560;

pub fn main() anyerror!void {
    rl.initWindow(screen_w, screen_h, "hh-scope");
    defer rl.closeWindow();

    var gpa = std.heap.GeneralPurposeAllocator(.{}){};
    const allocator = gpa.allocator();
    defer {
        const check = gpa.deinit();
        switch (check) {
            .leak => std.debug.print("Allocator deinit error\n", .{}),
            .ok => {},
        }
    }

    const server_args = [_][]const u8{ "./game-server", "-debug-tickmode", "realtime" };
    var proc = Child.init(&server_args, allocator);
    proc.stdout_behavior = .Pipe;
    proc.stderr_behavior = .Inherit;
    var state = State{};

    var thread = try start_server(allocator, &proc, &state);

    state.initFinished.wait();
    rl.setTargetFPS(rl.getMonitorRefreshRate(rl.getCurrentMonitor())); // FIXME: I hate this, do explicit sync &| vsync in the render loop.

    const plane_img = rl.loadTexture("assets/plane_1.png");
    const plane_w: f32 = @floatFromInt(plane_img.width);
    const plane_h: f32 = @floatFromInt(plane_img.height);
    const plane_size = rl.Rectangle{ .x = 0, .y = 0, .width = plane_w, .height = plane_h };

    var input_state: InputState = .none;

    while (!rl.windowShouldClose()) { // Detect window close button or ESC key
        rl.beginDrawing();
        const frameClock: i64 = @intCast(state.timer.read());

        const mouse_pos = rl.getMousePosition();
        switch (input_state) {
            .plane_target => blk: {
                if (rl.isMouseButtonReleased(rl.MouseButton.mouse_button_left)) {
                    // TODO: emit heading change event
                    input_state = .none;
                    break :blk;
                }
                input_state.plane_target.current = mouse_pos;
            },
            .none => {
                if (rl.isMouseButtonPressed(rl.MouseButton.mouse_button_left)) {
                    const clicked = Plane.intersecting_plane(&state, plane_size, mouse_pos);
                    if (clicked) |p| {
                        input_state = .{ .plane_target = .{
                            .plane_id = p.id,
                            .start = mouse_pos,
                            .current = mouse_pos,
                        } };
                    }
                }
            },
        }

        defer rl.endDrawing();
        defer rl.drawFPS(8, 8); // always draw last

        rl.clearBackground(rl.Color.white);

        state.mu.lock();
        const nanosPerTick = std.time.ns_per_s / state.tickRate;
        const deltans = frameClock - @as(i64, state.now) * nanosPerTick;
        state.deltaTicks = @as(f32, @floatFromInt(deltans)) / @as(f32, @floatFromInt(nanosPerTick));

        var highlighted_plane: Plane = undefined;
        for (state.planes) |plane| {
            var highlight = false;
            if (input_state == .plane_target and input_state.plane_target.plane_id == plane.id) {
                highlight = true;
                highlighted_plane = plane;
            }
            try plane.draw(allocator, &state, plane_img, plane_size, highlight, true);
        }

        if (input_state == .plane_target) {
            const loc = highlighted_plane.canvas_loc(plane_size, &state);
            const center = rl.Vector2{ .x = loc.x, .y = loc.y };
            rl.drawLineEx(center, input_state.plane_target.current, 4, rl.Color.red);
        }

        state.mu.unlock();
    }

    _ = try proc.kill();
    thread.join();
}

const InputPlaneTarget = struct {
    plane_id: u32,
    start: rl.Vector2,
    current: rl.Vector2,
};

const InputState = union(enum) { plane_target: InputPlaneTarget, none };

const XY = struct { x: f32 = 0, y: f32 = 0 };

const Plane = struct {
    id: u32 = 0,
    pos: XY = .{},
    want_heading: u16 = 0,
    heading: u16 = 0,

    fn draw(self: Plane, allocator: Allocator, state: *State, img: rl.Texture, src: rl.Rectangle, highlight: bool, draw_debug: bool) !void {
        const origin = rl.Vector2{ .x = src.width / 2, .y = src.height / 2 };
        const loc = self.canvas_loc(src, state);
        rl.drawTexturePro(img, src, loc, origin, self.rot(), rl.Color.white);

        // change to top right
        var loc_origin = loc;
        loc_origin.x -= loc.width / 2;
        loc_origin.y -= loc.height / 2;
        if (highlight) {
            rl.drawRectangleRoundedLinesEx(loc_origin, 64, 64, 4, rl.Color.red);
        }

        if (draw_debug) {
            var text_pos = rl.Vector2{ .x = loc_origin.x + loc.width, .y = loc_origin.y };
            text_pos.x = std.math.floor(text_pos.x);
            text_pos.y = std.math.floor(text_pos.y);

            const debug_text = try std.fmt.allocPrintZ(allocator, "id={}, pos=[{d}, {d}]", .{ self.id, self.pos.x, self.pos.y });
            rl.drawTextEx(rl.getFontDefault(), debug_text, text_pos, 16, 1, rl.Color.red);
            allocator.free(debug_text);
        }
    }

    fn intersecting_plane(state: *State, src: rl.Rectangle, mouse_pos: rl.Vector2) ?Plane {
        for (state.planes) |p| {
            const loc = p.canvas_loc(src, state);
            const center = rl.Vector2{ .x = loc.x, .y = loc.y };
            if (rl.checkCollisionPointCircle(mouse_pos, center, src.width / 2)) {
                return p;
            }
        }
        return null;
    }

    // returns the center position in screen-space of the plane.
    fn canvas_loc(self: Plane, src: rl.Rectangle, state: *State) rl.Rectangle {
        const rad = @as(f32, @floatFromInt(self.heading)) / 65536 * std.math.tau;
        const distance = state.deltaTicks * state.planeSpeed;
        const x = self.pos.x + std.math.sin(rad) * distance;
        const y = self.pos.y + std.math.cos(rad) * distance;
        // FIXME: go does interpolation of turn radius when turning, zig don't but it still looks fine but might be worth it.
        return rl.Rectangle{
            .x = (x + canvas_w / 2) * screen_w / canvas_w,
            .y = (-y + canvas_h / 2) * screen_h / canvas_h,
            .width = src.width,
            .height = src.height,
        };
    }

    fn rot(self: Plane) f32 {
        const deg: f32 = @as(f32, @floatFromInt(self.heading)) / 65536;
        return deg * 360;
    }
};

const State = struct {
    now: u32 = 0,
    deltaTicks: f32 = 0,
    tickRate: u32 = 0,
    planeSpeed: f32 = 0,
    planes: []Plane = &[_]Plane{},
    mu: Thread.Mutex = .{},
    initFinished: Thread.Semaphore = .{},
    timer: std.time.Timer = undefined,
};

fn start_server(allocator: Allocator, proc: *Child, state: *State) !Thread {
    const thread = try Thread.spawn(.{}, read_data, .{ allocator, proc, state });
    return thread;
}

fn read_data(allocator: Allocator, proc: *Child, state: *State) !void {
    try proc.spawn();

    const out = proc.stdout orelse return;
    var buffered_state = State{};

    var init = [_]u8{0} ** (4 + 1 + 4);
    _ = try out.readAll(&init);
    state.timer = try std.time.Timer.start(); // try to synchronize clock good enough with the server
    state.tickRate = r_u32(init[0..4]);
    if (init[4] >= 1 << 5) {
        return error.OutOfRange;
    }
    const subPixelFactor: f32 = @floatFromInt(@as(i32, 1) << @intCast(init[4]));
    state.planeSpeed = r_f32(init[5..9]) / subPixelFactor;
    state.initFinished.post();

    while (proc.term == null) {
        var header: [8]u8 = [_]u8{0} ** 8;
        _ = out.readAll(&header) catch break;

        const plane_count = r_u32(header[4..8]);
        const raw_planes = try allocator.alloc(u8, 16 * plane_count);
        defer allocator.free(raw_planes);

        _ = out.readAll(raw_planes) catch break;

        const now = r_u32(header[0..4]);
        buffered_state.now = now;

        const old = buffered_state.planes;
        buffered_state.planes = try allocator.alloc(Plane, plane_count);

        const plane_size = 4 + // id
            4 + // x
            4 + // y
            2 + // wantHeading
            2; // heading

        for (0..plane_count) |i| {
            const offset = plane_size * i;

            const id = r_u32(raw_planes[offset .. offset + 4]);
            const x = r_f32(raw_planes[offset + 4 .. offset + 8]) / subPixelFactor;
            const y = r_f32(raw_planes[offset + 8 .. offset + 12]) / subPixelFactor;

            const want_heading = r_u16(raw_planes[offset + 12 .. offset + 14]);
            const heading = r_u16(raw_planes[offset + 14 .. offset + 16]);

            buffered_state.planes[i].id = id;
            buffered_state.planes[i].pos.x = x;
            buffered_state.planes[i].pos.y = y;
            buffered_state.planes[i].want_heading = want_heading;
            buffered_state.planes[i].heading = heading;
        }

        state.mu.lock();
        state.*.now = buffered_state.now;
        state.*.planes = buffered_state.planes;
        allocator.free(old);
        state.mu.unlock();
    }

    allocator.free(buffered_state.planes);
}

fn r_u16(b: []u8) u16 {
    return @as(u16, b[0]) | @as(u16, b[1]) << 8;
}

fn r_u32(b: []u8) u32 {
    return @as(u32, b[0]) | (@as(u32, b[1]) << 8) | (@as(u32, b[2]) << 16) | (@as(u32, b[3]) << 24);
}

fn r_f32(b: []u8) f32 {
    return @floatFromInt(r_i32(b));
}

fn r_i32(b: []u8) i32 {
    return @as(i32, b[0]) | (@as(i32, b[1]) << 8) | (@as(i32, b[2]) << 16) | (@as(i32, b[3]) << 24);
}
