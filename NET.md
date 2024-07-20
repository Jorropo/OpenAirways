# Networking ideas

This is designed around feeling good in awful situations. This is likely to be exploitable by clients in a couple ways.

## Reliable Commits

Each client maintains two game states:
- Commited
- Live

The live state is in the future compared to the commited state. It is what is shown to the zig client.

There is a command buffer between commit and live, it is indexed by ticks id so that we know which commands act on each tick.

When a new command is sent, it includes live's tick id (now) and is added to the command buffer, then when a command is received it is added to the command buffer as it's indicated tick id, live is thrown away and a new live is created by starting at commit and replaying everyting in the command buffer at the correct tick ids.

After live increments, the clients send a commit item to the server. This let the server know how far away each client is, when all clients certainly reached some tick in time, it is commited, a confirmation is sent to the clients and we wont receive commands that far back anymore.

Note: on a reliable transport we probably don't need to transmit tick ids, we can diff instead.

Note: we need a way for clients to decide how much in the future they should be. This should be based on RTT and computed by the server so that everyone sees the same thing on the screen at the same time.

Note: the command buffer should have a maximum length, if a client is so slow it can't keep up, kick it out and resynchronise from a fresh copy, possibly on a new connection.

## (Optional) Unreliable Shortcuts

A full mesh is created between clients using an unreliable transport, [DSCP EF](https://en.wikipedia.org/wiki/Differentiated_services#Expedited_Forwarding) should be used.

When a client performs an action, along the reliable commits pipeline, it can send it along with it's live now value to all the other clients.

When clients receive unreliable packets they apply it to their own future buffer at the indicated now time and perform a rollback.
Theses are discarded once the commit pipeline reach that point (altho an identical command probably exists in the reliable pipeline).

The point is to not have Head-Of-Line issues during packet loss events, one command is lost but all the other commands still go through.

If an unreliable packet is lost, the user wont see the command early but it will see it once received from the reliable pipeline.

The commands also skip the server and use QoS which might provide a better ping and snappier feeling.

Each client can decide to use or not shortcuts, connectivity in the mesh might also be partial, in this case everything goes through the normal reliable pipeline.