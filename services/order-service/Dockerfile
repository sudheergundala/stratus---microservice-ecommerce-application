FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /app
# Dependencies first: this layer stays cached until pom.xml changes
COPY pom.xml ./
RUN mvn -B -q dependency:go-offline
# Then the source code, which changes on every commit
COPY src ./src
# -DskipTests will skip the tests while building and it only runs the tests in CI.
RUN mvn -B -q package -DskipTests
# the mvn package produces one fat .jar file (target/order-service.jar). It contains all the libraries (Spring, Tomcat, Jackson) plus application code. 
# Copied as one file, it becomes one image layer, and a one-line code change means pushing and pulling the entire 20+ MB again on every deploy, to every node.
# Split the fat JAR into layers(dependencies,spring-boot-loader,snapshot-dependencies,application): libraries change rarely, application code every commit
RUN cp target/order-service.jar application.jar \
 && java -Djarmode=tools -jar application.jar extract --layers --destination extracted

 # .........Stage2 : runtime ............#
 # Java 21 runtime only: no shell, no package manager. ENTRYPOINT is already "java -jar".
 FROM gcr.io/distroless/java21-debian13:nonroot
 WORKDIR /app
 COPY --from=build /app/extracted/dependencies/ ./
 COPY --from=build /app/extracted/spring-boot-loader/ ./
 COPY --from=build /app/extracted/snapshot-dependencies/ ./
 COPY --from=build /app/extracted/application/ ./
 # The JVM reads the container's memory limit. By default, though, it allows itself only 25% of that limit for the heap, where your objects live.
 # 75% sits mostly unused, and the app hits OutOfMemoryError or slows down from constant garbage collection, even though the container has room.
 # Give the Java heap 75% of the container's memory limit (the default is only 25%)
 ENV JAVA_TOOL_OPTIONS="-XX:MaxRAMPercentage=75"
 USER 65532:65532
 EXPOSE 8082
 CMD [ "application.jar" ]